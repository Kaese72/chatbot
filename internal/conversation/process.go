package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Kaese72/chatbot/internal/llm"
	"github.com/Kaese72/chatbot/internal/persistence"
	"github.com/Kaese72/chatbot/restmodels"
	log "github.com/Kaese72/huemie-lib/logging"
	"github.com/anthropics/anthropic-sdk-go"
)

// process is the README's "Architecture" section made literal. It owns a
// conversation's processing lock (lockID) from the moment it's called
// until it releases it just before returning, runs entirely on a detached
// background goroutine, and is the only place that talks to the LLM.
// apiKey is the active Anthropic API key fetched from the database by the
// caller (New/Input) before this goroutine was started.
func (s *Service) process(ctx context.Context, conversationID int64, lockID string, apiKey string) {
	terminationCh, unsubscribe := s.terminations.Subscribe(conversationID)
	defer unsubscribe()

	entries, err := s.db.ListDialogEntries(ctx, conversationID, 0)
	if err != nil {
		log.Error("failed to load conversation history: "+err.Error(), map[string]interface{}{"conversation-id": conversationID})
		return
	}

	// Recover from any tool call left unresolved by a previous, interrupted
	// processing attempt (termination or a lost/expired lock) before doing
	// anything else, by resolving it exactly as if the interruption had
	// just happened now.
	for _, pending := range findUnresolvedToolCalls(entries) {
		recovered, err := s.append(ctx, conversationID, lockID, buildToolResponseEntry(pending.ToolUseID, pending.Kind, false, interruptedMessage))
		if err != nil {
			log.Error("failed to recover unresolved tool call: "+err.Error(), map[string]interface{}{"conversation-id": conversationID})
			return
		}
		entries = append(entries, recovered)
	}

	messages := buildHistory(entries)
	iterations := 0

mainLoop:
	for {
		if isTerminated(terminationCh) {
			s.handleTermination(ctx, conversationID, lockID)
			return
		}

		iterations++
		if iterations > s.maxToolLoopIterations {
			s.appendBestEffort(ctx, conversationID, lockID, agentErrorEntry(fmt.Sprintf(
				"the tool-call loop exceeded the maximum of %d iterations without reaching a final answer", s.maxToolLoopIterations,
			)))
			break mainLoop
		}

		message, err := s.runTurnWithRetry(ctx, conversationID, lockID, apiKey, terminationCh, messages)
		if err != nil {
			if errors.Is(err, persistence.ErrLockLost) {
				return
			}
			if errors.Is(err, errTerminated) {
				s.handleTermination(ctx, conversationID, lockID)
				return
			}
			log.Error("LLM request failed: "+err.Error(), map[string]interface{}{"conversation-id": conversationID})
			s.appendBestEffort(ctx, conversationID, lockID, agentErrorEntry("LLM request failed: "+err.Error()))
			break mainLoop
		}

		switch message.StopReason {
		case anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
			break mainLoop

		case anthropic.StopReasonMaxTokens:
			s.appendBestEffort(ctx, conversationID, lockID, agentErrorEntry("the response was truncated: reached the maximum response length"))
			break mainLoop

		case anthropic.StopReasonRefusal:
			s.appendBestEffort(ctx, conversationID, lockID, agentErrorEntry("the model declined to respond"))
			break mainLoop

		case anthropic.StopReasonModelContextWindowExceeded:
			s.appendBestEffort(ctx, conversationID, lockID, agentErrorEntry("the conversation exceeded the model's context window"))
			break mainLoop

		case anthropic.StopReasonPauseTurn:
			// A long-running server-side tool sequence paused; resend the
			// conversation as-is (no tool_result to add -- no client tool
			// was involved) so the server can resume where it left off.
			messages = append(messages, message.ToParam())
			continue mainLoop

		case anthropic.StopReasonToolUse:
			if isTerminated(terminationCh) {
				// The tool_use blocks were already persisted as *_CALL
				// entries by persistBlock above; leaving them unresolved
				// here is safe, since the next BeginInput will recover
				// them exactly like any other interrupted turn.
				s.handleTermination(ctx, conversationID, lockID)
				return
			}

			messages = append(messages, message.ToParam())

			results := []anthropic.ContentBlockParamUnion{}
			interrupted := false
			for _, block := range message.Content {
				toolUse, ok := block.AsAny().(anthropic.ToolUseBlock)
				if !ok {
					continue
				}

				if !interrupted && isTerminated(terminationCh) {
					interrupted = true
				}

				var success bool
				var resultText string
				if interrupted {
					success, resultText = false, interruptedMessage
				} else {
					success, resultText = s.dispatcher.Dispatch(ctx, toolUse.Name, toolUse.Input)
				}

				if _, err := s.append(ctx, conversationID, lockID, buildToolResponseEntry(toolUse.ID, llm.ClassifyTool(toolUse.Name), success, resultText)); err != nil {
					if errors.Is(err, persistence.ErrLockLost) {
						return
					}
					log.Error("failed to record tool response: "+err.Error(), map[string]interface{}{"conversation-id": conversationID})
					return
				}
				results = append(results, anthropic.NewToolResultBlock(toolUse.ID, resultText, !success))
			}

			if interrupted {
				s.handleTermination(ctx, conversationID, lockID)
				return
			}

			messages = append(messages, anthropic.NewUserMessage(results...))
			continue mainLoop

		default:
			s.appendBestEffort(ctx, conversationID, lockID, agentErrorEntry(fmt.Sprintf("unexpected stop reason %q", message.StopReason)))
			break mainLoop
		}
	}

	if err := s.db.ReleaseLock(ctx, conversationID, lockID); err != nil {
		log.Error("failed to release conversation lock: "+err.Error(), map[string]interface{}{"conversation-id": conversationID})
	}
	s.publishUpdated(conversationID)
}

// handleTermination records that a termination request was honored, hands
// the initiative back to the user, and notifies subscribers. It is the
// counterpart to the RabbitMQ "terminate" event described in the README's
// "Terminating agent deliberation" section: "adding a DialogEntry for the
// termination once it is successful".
func (s *Service) handleTermination(ctx context.Context, conversationID int64, lockID string) {
	if _, err := s.append(ctx, conversationID, lockID, persistence.NewDialogEntry{
		Type:       restmodels.DialogEntryTypeUserStop,
		Initiative: restmodels.InitiativeUser,
		Payload:    struct{}{},
	}); err != nil && !errors.Is(err, persistence.ErrLockLost) {
		log.Error("failed to record termination: "+err.Error(), map[string]interface{}{"conversation-id": conversationID})
	}
	if err := s.db.ReleaseLock(ctx, conversationID, lockID); err != nil {
		log.Error("failed to release conversation lock after termination: "+err.Error(), map[string]interface{}{"conversation-id": conversationID})
	}
	s.publishUpdated(conversationID)
}

// persistBlock records a single completed content block from the LLM as
// the matching DialogEntry, per the README's "Read entire content block
// from LLM SSE stream, save the content block" step.
func (s *Service) persistBlock(ctx context.Context, conversationID int64, lockID string, block anthropic.ContentBlockUnion) error {
	switch block.Type {
	case "text":
		_, err := s.append(ctx, conversationID, lockID, persistence.NewDialogEntry{
			Type:       restmodels.DialogEntryTypeAgentMessage,
			Initiative: restmodels.InitiativeAgent,
			Payload:    restmodels.AgentMessagePayload{Text: block.Text},
		})
		return err

	case "tool_use":
		return s.persistToolCall(ctx, conversationID, lockID, block.ID, block.Name, block.Input)

	default:
		// Thinking is explicitly disabled and no server-side tools are
		// configured, so no other block type should ever be produced. If
		// one is, surface it loudly rather than silently discarding data
		// the DialogEntry schema (and this conversation's replay logic)
		// has no slot for.
		return fmt.Errorf("unexpected content block type %q from LLM", block.Type)
	}
}

func (s *Service) persistToolCall(ctx context.Context, conversationID int64, lockID string, toolUseID string, name string, input json.RawMessage) error {
	switch llm.ClassifyTool(name) {
	case llm.ToolKindDeviceCapabilityTrigger:
		var in llm.DeviceCapabilityTriggerInput
		_ = json.Unmarshal(input, &in) // best-effort: malformed input is still recorded, and independently fails at dispatch with a normal tool_result error
		_, err := s.append(ctx, conversationID, lockID, persistence.NewDialogEntry{
			Type:       restmodels.DialogEntryTypeAgentDeviceCapabilityTriggerCall,
			Initiative: restmodels.InitiativeAgent,
			Payload: restmodels.DeviceCapabilityTriggerCallPayload{
				ToolUseID: toolUseID, DeviceID: in.DeviceID, Capability: in.Capability, Args: in.Args,
			},
		})
		return err

	case llm.ToolKindGroupCapabilityTrigger:
		var in llm.GroupCapabilityTriggerInput
		_ = json.Unmarshal(input, &in)
		_, err := s.append(ctx, conversationID, lockID, persistence.NewDialogEntry{
			Type:       restmodels.DialogEntryTypeAgentGroupCapabilityTriggerCall,
			Initiative: restmodels.InitiativeAgent,
			Payload: restmodels.GroupCapabilityTriggerCallPayload{
				ToolUseID: toolUseID, GroupID: in.GroupID, Capability: in.Capability, Args: in.Args,
			},
		})
		return err

	default:
		_, err := s.append(ctx, conversationID, lockID, persistence.NewDialogEntry{
			Type:       restmodels.DialogEntryTypeAgentGenericToolCall,
			Initiative: restmodels.InitiativeAgent,
			Payload:    restmodels.GenericToolCallPayload{ToolUseID: toolUseID, ToolName: name, Input: input},
		})
		return err
	}
}

func agentErrorEntry(message string) persistence.NewDialogEntry {
	return persistence.NewDialogEntry{
		Type:       restmodels.DialogEntryTypeAgentError,
		Initiative: restmodels.InitiativeAgent,
		Payload:    restmodels.AgentErrorPayload{Message: message},
	}
}
