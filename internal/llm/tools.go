package llm

import (
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
)

// Tool names. These are the only tools the LLM is ever given, and they are
// the single source of truth this service uses to decide how a completed
// tool_use content block (and its eventual result) should be persisted:
// ToolTriggerDeviceCapability/ToolTriggerGroupCapability get the strictly
// structured DialogEntry treatment described in the README; everything
// else — currently ToolListDevices and ToolListGroups — falls back to the
// generic, free-text-blob treatment.
const (
	ToolListDevices             = "list_devices"
	ToolListGroups              = "list_groups"
	ToolTriggerDeviceCapability = "trigger_device_capability"
	ToolTriggerGroupCapability  = "trigger_group_capability"
)

// ToolKind classifies a tool call for DialogEntry persistence purposes.
type ToolKind int

const (
	ToolKindGeneric ToolKind = iota
	ToolKindDeviceCapabilityTrigger
	ToolKindGroupCapabilityTrigger
)

// ClassifyTool reports how a tool call by this name must be persisted. Any
// name it doesn't recognize (including tools the model hallucinates) is
// treated as generic, matching the README's fallback: "Currently A LOT
// goes into the AGENT_GENERIC_TOOL_CALL... For all other types we expect
// all information to be strictly structured" — i.e. generic is the
// default, and only the two capability-trigger tools opt into structure.
func ClassifyTool(name string) ToolKind {
	switch name {
	case ToolTriggerDeviceCapability:
		return ToolKindDeviceCapabilityTrigger
	case ToolTriggerGroupCapability:
		return ToolKindGroupCapabilityTrigger
	default:
		return ToolKindGeneric
	}
}

// DeviceCapabilityTriggerInput is the JSON shape the
// trigger_device_capability tool accepts, per its schema in
// ToolDefinitions.
type DeviceCapabilityTriggerInput struct {
	DeviceID   int             `json:"device_id"`
	Capability string          `json:"capability"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// GroupCapabilityTriggerInput is the JSON shape the
// trigger_group_capability tool accepts, per its schema in
// ToolDefinitions.
type GroupCapabilityTriggerInput struct {
	GroupID    int             `json:"group_id"`
	Capability string          `json:"capability"`
	Args       json.RawMessage `json:"args,omitempty"`
}

// ToolDefinitions is the fixed set of tools offered to the LLM on every
// turn of every conversation, per the README's "forwards them to an LLM
// together with the available tools". Discovery (which devices/groups and
// capabilities exist) is itself a tool call (list_devices/list_groups)
// rather than being embedded in the system prompt, so the schema here does
// not need to change as devices come and go.
func ToolDefinitions() []anthropic.ToolUnionParam {
	return []anthropic.ToolUnionParam{
		{OfTool: &anthropic.ToolParam{
			Name:        ToolListDevices,
			Description: anthropic.String("List every device known to the system: its numeric ID, current attribute values, and the capabilities it supports (with their argument schemas). Call this to discover which devices exist and what you can do with them before triggering a capability."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolListGroups,
			Description: anthropic.String("List every device group known to the system: its numeric ID, member device IDs, and the capabilities it supports (with their argument schemas). Call this to discover which groups exist before triggering a capability on one."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolTriggerDeviceCapability,
			Description: anthropic.String("Trigger a capability on a single device (for example, turning it on or setting its brightness). Call list_devices first to find the device's numeric ID and the exact capability name and argument shape it supports."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"device_id": map[string]any{
						"type":        "integer",
						"description": "The device's numeric ID, from list_devices.",
					},
					"capability": map[string]any{
						"type":        "string",
						"description": "The exact capability name to trigger, from list_devices.",
					},
					"args": map[string]any{
						"type":        "object",
						"description": "Capability-specific arguments, if the capability's argument specs (from list_devices) call for any.",
					},
				},
				Required: []string{"device_id", "capability"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolTriggerGroupCapability,
			Description: anthropic.String("Trigger a capability on every device in a group at once. Call list_groups first to find the group's numeric ID and the exact capability name and argument shape it supports."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"group_id": map[string]any{
						"type":        "integer",
						"description": "The group's numeric ID, from list_groups.",
					},
					"capability": map[string]any{
						"type":        "string",
						"description": "The exact capability name to trigger, from list_groups.",
					},
					"args": map[string]any{
						"type":        "object",
						"description": "Capability-specific arguments, if the capability's argument specs (from list_groups) call for any.",
					},
				},
				Required: []string{"group_id", "capability"},
			},
			// A cache_control breakpoint on the last tool definition covers
			// the whole (fixed, identical-every-call) tools array -- see
			// internal/llm/client.go's matching breakpoints on the system
			// prompt and the last message of history.
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
	}
}
