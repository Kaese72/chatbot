package conversation

import (
	"encoding/json"
	"errors"

	"github.com/Kaese72/chatbot/internal/llm"
	"github.com/Kaese72/chatbot/internal/persistence"
	"github.com/Kaese72/chatbot/restmodels"
	"github.com/anthropics/anthropic-sdk-go"
)

// errTerminated is returned from an llm.BlockCallback to abort an in-flight
// LLM turn as soon as a termination signal is observed, per the README's
// requirement to check for termination "before taking action" at every
// opportunity -- including partway through consuming a single streamed
// response.
var errTerminated = errors.New("conversation terminated")

// interruptedMessage is recorded as the tool_result / *_RESPONSE content
// for any tool call left unresolved when a conversation is terminated or
// its processing lock is lost mid-turn, whether that happens while a tool
// call is still executing or is discovered as leftover state from a prior
// interrupted attempt when a new turn begins.
const interruptedMessage = "Interrupted before completion. Please retry if needed."

func isTerminated(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// pendingCall is a tool call whose DialogEntryTypeAgent*Call entry has no
// matching *Response entry yet.
type pendingCall struct {
	ToolUseID string
	Kind      llm.ToolKind
}

// findUnresolvedToolCalls scans a conversation's full DialogEntry history
// for *_CALL entries with no matching *_RESPONSE entry, in the order they
// were called. In normal operation this is always empty -- calls are
// always resolved before a turn returns to the user -- and it is only
// non-empty when a previous processing attempt for this conversation was
// interrupted (terminated, or lost its lock) partway through resolving a
// turn's tool calls.
func findUnresolvedToolCalls(entries []restmodels.DialogEntry) []pendingCall {
	order := []string{}
	kindByID := map[string]llm.ToolKind{}

	for _, e := range entries {
		switch e.Type {
		case restmodels.DialogEntryTypeAgentGenericToolCall:
			id := e.GenericToolCall.ToolUseID
			order = append(order, id)
			kindByID[id] = llm.ToolKindGeneric
		case restmodels.DialogEntryTypeAgentDeviceCapabilityTriggerCall:
			id := e.DeviceCapabilityTriggerCall.ToolUseID
			order = append(order, id)
			kindByID[id] = llm.ToolKindDeviceCapabilityTrigger
		case restmodels.DialogEntryTypeAgentGroupCapabilityTriggerCall:
			id := e.GroupCapabilityTriggerCall.ToolUseID
			order = append(order, id)
			kindByID[id] = llm.ToolKindGroupCapabilityTrigger
		case restmodels.DialogEntryTypeAgentGenericToolResponse:
			delete(kindByID, e.GenericToolResponse.ToolUseID)
		case restmodels.DialogEntryTypeAgentDeviceCapabilityTriggerResponse:
			delete(kindByID, e.DeviceCapabilityTriggerResponse.ToolUseID)
		case restmodels.DialogEntryTypeAgentGroupCapabilityTriggerResponse:
			delete(kindByID, e.GroupCapabilityTriggerResponse.ToolUseID)
		}
	}

	pending := []pendingCall{}
	for _, id := range order {
		if kind, ok := kindByID[id]; ok {
			pending = append(pending, pendingCall{ToolUseID: id, Kind: kind})
			delete(kindByID, id) // a duplicated tool_use ID should never happen; guard against double-counting if it somehow does
		}
	}
	return pending
}

// buildToolResponseEntry constructs the persistence.NewDialogEntry for a
// tool call's outcome, choosing the DialogEntryType that matches kind so
// that every *_CALL is always paired with a *_RESPONSE of the
// corresponding structured or generic type.
func buildToolResponseEntry(toolUseID string, kind llm.ToolKind, success bool, message string) persistence.NewDialogEntry {
	switch kind {
	case llm.ToolKindDeviceCapabilityTrigger:
		payload := restmodels.CapabilityTriggerResponsePayload{ToolUseID: toolUseID, Success: success}
		if !success {
			payload.ErrorMessage = &message
		}
		return persistence.NewDialogEntry{
			Type:       restmodels.DialogEntryTypeAgentDeviceCapabilityTriggerResponse,
			Initiative: restmodels.InitiativeAgent,
			Payload:    payload,
		}
	case llm.ToolKindGroupCapabilityTrigger:
		payload := restmodels.CapabilityTriggerResponsePayload{ToolUseID: toolUseID, Success: success}
		if !success {
			payload.ErrorMessage = &message
		}
		return persistence.NewDialogEntry{
			Type:       restmodels.DialogEntryTypeAgentGroupCapabilityTriggerResponse,
			Initiative: restmodels.InitiativeAgent,
			Payload:    payload,
		}
	default:
		return persistence.NewDialogEntry{
			Type:       restmodels.DialogEntryTypeAgentGenericToolResponse,
			Initiative: restmodels.InitiativeAgent,
			Payload: restmodels.GenericToolResponsePayload{
				ToolUseID: toolUseID,
				Output:    message,
				IsError:   !success,
			},
		}
	}
}

func capabilityResultText(p *restmodels.CapabilityTriggerResponsePayload) string {
	if p.Success {
		return "triggered"
	}
	if p.ErrorMessage != nil {
		return *p.ErrorMessage
	}
	return "failed"
}

// buildHistory replays a conversation's persisted DialogEntries into the
// message list the Anthropic API expects. Each relevant entry becomes one
// single-content-block MessageParam; consecutive same-role messages are
// combined into one turn by the API itself, so there is no need to
// hand-merge multi-block turns back together here -- see the "tool result
// placement" and "role distinction" design notes this mirrors.
func buildHistory(entries []restmodels.DialogEntry) []anthropic.MessageParam {
	messages := []anthropic.MessageParam{}
	for _, e := range entries {
		switch e.Type {
		case restmodels.DialogEntryTypeUserInput:
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(e.UserInput.Text)))

		case restmodels.DialogEntryTypeAgentMessage:
			messages = append(messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(e.AgentMessage.Text)))

		case restmodels.DialogEntryTypeAgentGenericToolCall:
			messages = append(messages, anthropic.NewAssistantMessage(
				anthropic.NewToolUseBlock(e.GenericToolCall.ToolUseID, json.RawMessage(e.GenericToolCall.Input), e.GenericToolCall.ToolName),
			))
		case restmodels.DialogEntryTypeAgentGenericToolResponse:
			messages = append(messages, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(e.GenericToolResponse.ToolUseID, e.GenericToolResponse.Output, e.GenericToolResponse.IsError),
			))

		case restmodels.DialogEntryTypeAgentDeviceCapabilityTriggerCall:
			input, _ := json.Marshal(llm.DeviceCapabilityTriggerInput{
				DeviceID:   e.DeviceCapabilityTriggerCall.DeviceID,
				Capability: e.DeviceCapabilityTriggerCall.Capability,
				Args:       e.DeviceCapabilityTriggerCall.Args,
			})
			messages = append(messages, anthropic.NewAssistantMessage(
				anthropic.NewToolUseBlock(e.DeviceCapabilityTriggerCall.ToolUseID, json.RawMessage(input), llm.ToolTriggerDeviceCapability),
			))
		case restmodels.DialogEntryTypeAgentDeviceCapabilityTriggerResponse:
			messages = append(messages, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(e.DeviceCapabilityTriggerResponse.ToolUseID, capabilityResultText(e.DeviceCapabilityTriggerResponse), !e.DeviceCapabilityTriggerResponse.Success),
			))

		case restmodels.DialogEntryTypeAgentGroupCapabilityTriggerCall:
			input, _ := json.Marshal(llm.GroupCapabilityTriggerInput{
				GroupID:    e.GroupCapabilityTriggerCall.GroupID,
				Capability: e.GroupCapabilityTriggerCall.Capability,
				Args:       e.GroupCapabilityTriggerCall.Args,
			})
			messages = append(messages, anthropic.NewAssistantMessage(
				anthropic.NewToolUseBlock(e.GroupCapabilityTriggerCall.ToolUseID, json.RawMessage(input), llm.ToolTriggerGroupCapability),
			))
		case restmodels.DialogEntryTypeAgentGroupCapabilityTriggerResponse:
			messages = append(messages, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(e.GroupCapabilityTriggerResponse.ToolUseID, capabilityResultText(e.GroupCapabilityTriggerResponse), !e.GroupCapabilityTriggerResponse.Success),
			))

		case restmodels.DialogEntryTypeUserStop, restmodels.DialogEntryTypeAgentError:
			// Audit-only: not part of the LLM's own turn history.
		}
	}
	return messages
}
