package restmodels

import (
	"encoding/json"
	"time"
)

// DialogEntryType is the discriminator described in the README's
// "DialogEntry" section. It determines which of the type-specific payload
// fields on DialogEntry is populated, and how the entry should be rendered.
type DialogEntryType string

const (
	// DialogEntryTypeUserInput is a query submitted by the user, either the
	// conversation-starting query or a follow-up sent while the user held
	// the initiative.
	DialogEntryTypeUserInput DialogEntryType = "USER_INPUT"
	// DialogEntryTypeUserStop records that a termination request was
	// received and acted upon by the replica handling the conversation.
	DialogEntryTypeUserStop DialogEntryType = "USER_STOP"
	// DialogEntryTypeAgentMessage is a completed text content block
	// produced by the LLM.
	DialogEntryTypeAgentMessage DialogEntryType = "AGENT_MESSAGE"
	// DialogEntryTypeAgentError records a processing error (a non-tool_use,
	// non-end_turn stop reason, or an unrecoverable internal failure).
	DialogEntryTypeAgentError DialogEntryType = "AGENT_ERROR"
	// DialogEntryTypeAgentGenericToolCall is any tool invocation other than
	// the two structured capability-trigger tools below. Per the README,
	// these are recorded as free-text blobs rather than strictly
	// structured data.
	DialogEntryTypeAgentGenericToolCall DialogEntryType = "AGENT_GENERIC_TOOL_CALL"
	// DialogEntryTypeAgentGenericToolResponse is the result of a generic
	// tool call.
	DialogEntryTypeAgentGenericToolResponse DialogEntryType = "AGENT_GENERIC_TOOL_RESPONSE"
	// DialogEntryTypeAgentDeviceCapabilityTriggerCall is a structured
	// device-store device capability trigger requested by the LLM.
	DialogEntryTypeAgentDeviceCapabilityTriggerCall DialogEntryType = "AGENT_DEVICE_CAPABILITY_TRIGGER_CALL"
	// DialogEntryTypeAgentDeviceCapabilityTriggerResponse is the structured
	// result of a device capability trigger.
	DialogEntryTypeAgentDeviceCapabilityTriggerResponse DialogEntryType = "AGENT_DEVICE_CAPABILITY_TRIGGER_RESPONSE"
	// DialogEntryTypeAgentGroupCapabilityTriggerCall is a structured
	// device-store group capability trigger requested by the LLM.
	DialogEntryTypeAgentGroupCapabilityTriggerCall DialogEntryType = "AGENT_GROUP_CAPABILITY_TRIGGER_CALL"
	// DialogEntryTypeAgentGroupCapabilityTriggerResponse is the structured
	// result of a group capability trigger.
	DialogEntryTypeAgentGroupCapabilityTriggerResponse DialogEntryType = "AGENT_GROUP_CAPABILITY_TRIGGER_RESPONSE"
	// DialogEntryTypeAgentInitiativeReleased records that agent processing
	// for this turn finished (normally, on error, or by hitting the
	// tool-loop iteration limit) and initiative was handed back to the
	// user. Unlike the other AGENT_* types it carries no payload -- its
	// presence in the stream is the fact being recorded. It is what lets a
	// client tell "it's your turn again" purely from the DialogEntry
	// stream, without separately polling Conversation.Initiative/Status.
	// Termination has its own equivalent marker, DialogEntryTypeUserStop,
	// and this type is never used for that case.
	DialogEntryTypeAgentInitiativeReleased DialogEntryType = "AGENT_INITIATIVE_RELEASED"
)

// UserInputPayload backs DialogEntryTypeUserInput.
type UserInputPayload struct {
	Text string `json:"text"`
}

// AgentMessagePayload backs DialogEntryTypeAgentMessage.
type AgentMessagePayload struct {
	Text string `json:"text"`
}

// AgentErrorPayload backs DialogEntryTypeAgentError.
type AgentErrorPayload struct {
	Message string `json:"message"`
}

// GenericToolCallPayload backs DialogEntryTypeAgentGenericToolCall. Input is
// recorded as a raw text blob (the tool's JSON input, unparsed) per the
// README's "A LOT goes into the AGENT_GENERIC_TOOL_CALL" note.
type GenericToolCallPayload struct {
	ToolUseID string          `json:"tool-use-id"`
	ToolName  string          `json:"tool-name"`
	Input     json.RawMessage `json:"input"`
}

// GenericToolResponsePayload backs DialogEntryTypeAgentGenericToolResponse.
type GenericToolResponsePayload struct {
	ToolUseID string `json:"tool-use-id"`
	Output    string `json:"output"`
	IsError   bool   `json:"is-error"`
}

// DeviceCapabilityTriggerCallPayload backs
// DialogEntryTypeAgentDeviceCapabilityTriggerCall.
type DeviceCapabilityTriggerCallPayload struct {
	ToolUseID  string          `json:"tool-use-id"`
	DeviceID   int             `json:"device-id"`
	Capability string          `json:"capability"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// GroupCapabilityTriggerCallPayload backs
// DialogEntryTypeAgentGroupCapabilityTriggerCall.
type GroupCapabilityTriggerCallPayload struct {
	ToolUseID  string          `json:"tool-use-id"`
	GroupID    int             `json:"group-id"`
	Capability string          `json:"capability"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// CapabilityTriggerResponsePayload backs both
// DialogEntryTypeAgentDeviceCapabilityTriggerResponse and
// DialogEntryTypeAgentGroupCapabilityTriggerResponse — device-store returns
// no structured success payload beyond HTTP status, so the only outcome
// worth recording is success/failure plus an optional error message.
type CapabilityTriggerResponsePayload struct {
	ToolUseID    string  `json:"tool-use-id"`
	Success      bool    `json:"success"`
	ErrorMessage *string `json:"error-message,omitempty"`
}

// DialogEntry is a single message or piece of information in a
// Conversation's back-and-forth. Exactly the payload field(s) matching Type
// are populated; the rest are omitted from JSON output.
type DialogEntry struct {
	// ID is the global, monotonically increasing primary key described in
	// the README. Gaps from a single Conversation's perspective are
	// expected and harmless.
	ID             int64           `json:"id"`
	Created        time.Time       `json:"created"`
	ConversationID int64           `json:"conversation-id"`
	Type           DialogEntryType `json:"type"`
	Initiative     Initiative      `json:"initiative"`

	UserInput                       *UserInputPayload                   `json:"user-input,omitempty"`
	AgentMessage                    *AgentMessagePayload                `json:"agent-message,omitempty"`
	AgentError                      *AgentErrorPayload                  `json:"agent-error,omitempty"`
	GenericToolCall                 *GenericToolCallPayload             `json:"generic-tool-call,omitempty"`
	GenericToolResponse             *GenericToolResponsePayload         `json:"generic-tool-response,omitempty"`
	DeviceCapabilityTriggerCall     *DeviceCapabilityTriggerCallPayload `json:"device-capability-trigger-call,omitempty"`
	DeviceCapabilityTriggerResponse *CapabilityTriggerResponsePayload   `json:"device-capability-trigger-response,omitempty"`
	GroupCapabilityTriggerCall      *GroupCapabilityTriggerCallPayload  `json:"group-capability-trigger-call,omitempty"`
	GroupCapabilityTriggerResponse  *CapabilityTriggerResponsePayload   `json:"group-capability-trigger-response,omitempty"`
}
