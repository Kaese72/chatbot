// Package llm wraps the Anthropic Messages API for a single conversation
// turn: sending history + tools, streaming the response, and handing each
// completed content block to a callback as soon as it finishes streaming
// (README step 6: "Read entire content block from LLM SSE stream, save the
// content block... Do this until the LLM have finished giving us
// messages").
package llm

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// maxTokens caps a single turn's output. Every turn is streamed, so the
// SDK's non-streaming timeout guard doesn't apply; 64000 leaves generous
// room for tool-heavy turns without being the full 128k ceiling.
const maxTokens = 64000

// SystemPrompt is the fixed system prompt for every conversation. It is
// intentionally short: capability and device discovery happens through
// tool calls (list_devices/list_groups), not by enumerating them here,
// so the prompt does not need to be rebuilt per conversation or grow with
// the number of devices.
const SystemPrompt = `You are the Huemie home automation assistant, running as a service inside the home appliance you help control.

You act as your own authenticated user; every action you take through your tools is attributed to you, not to the person you are talking to. Use list_devices and list_groups to discover what devices and groups exist, what their current state is, and what capabilities they support (including required arguments), before calling trigger_device_capability or trigger_group_capability. Only use capability names and arguments you have actually seen returned by list_devices or list_groups -- never guess a capability name or its arguments.

Be concise. Confirm destructive or hard-to-reverse actions in plain language after you take them, but do not ask for permission before taking a routine, reversible action the user clearly asked for.`

// Client sends conversation turns to the Anthropic Messages API.
type Client struct {
	anthropicClient anthropic.Client
	model           string
}

func NewClient(apiKey string, model string) *Client {
	return &Client{
		anthropicClient: anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:           model,
	}
}

// BlockCallback is invoked once per completed content block, in the order
// the blocks finished streaming, immediately after each one finishes
// (content_block_stop). Returning a non-nil error aborts the turn
// immediately without processing any further events -- used so the caller
// can react to a lost processing lock or a termination request mid-stream,
// per the README's requirement to check before taking any further action.
type BlockCallback func(index int64, block anthropic.ContentBlockUnion) error

// RunTurn sends messages (with the fixed tool set and system prompt) to the
// LLM and streams the response, invoking onBlock as each content block
// finishes. It returns the fully accumulated message even when onBlock
// aborts the turn early, so the caller can still see whatever was
// completed before the abort.
//
// Thinking is explicitly disabled on every call: the DialogEntry schema
// (see restmodels.DialogEntryType) has no slot for thinking blocks, and
// several Claude models think by default, so this must be an explicit
// choice rather than left to the model's default.
func (c *Client) RunTurn(ctx context.Context, messages []anthropic.MessageParam, onBlock BlockCallback) (anthropic.Message, error) {
	params := anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: SystemPrompt}},
		Messages:  messages,
		Tools:     ToolDefinitions(),
		Thinking:  anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
	}

	stream := c.anthropicClient.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	message := anthropic.Message{}
	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return message, fmt.Errorf("accumulating LLM stream event: %w", err)
		}
		if stopEvent, ok := event.AsAny().(anthropic.ContentBlockStopEvent); ok {
			if err := onBlock(stopEvent.Index, message.Content[stopEvent.Index]); err != nil {
				return message, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return message, fmt.Errorf("LLM stream error: %w", err)
	}
	return message, nil
}
