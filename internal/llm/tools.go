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
// else — currently the list/search tools and the automation rule tools —
// falls back to the generic, free-text-blob treatment.
const (
	ToolListDevices             = "list_devices"
	ToolListGroups              = "list_groups"
	ToolTriggerDeviceCapability = "trigger_device_capability"
	ToolTriggerGroupCapability  = "trigger_group_capability"
	ToolFuzzyFindGroup          = "fuzzy_find_group"

	// Automation (ittt-orchestrator) rule tools. All generic for
	// DialogEntry persistence purposes.
	ToolListRules     = "list_rules"
	ToolGetRule       = "get_rule"
	ToolGetRuleSchema = "get_rule_schema"
	ToolCreateRule    = "create_rule"
	ToolUpdateRule    = "update_rule"
	ToolDeleteRule    = "delete_rule"
	ToolEvaluateRule  = "evaluate_rule"
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

// FuzzyFindGroupInput is the JSON shape the fuzzy_find_group tool accepts,
// per its schema in ToolDefinitions.
type FuzzyFindGroupInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// RuleIDInput is the JSON shape of the rule tools that take only a rule ID
// (get_rule, delete_rule, evaluate_rule).
type RuleIDInput struct {
	RuleID int `json:"rule_id"`
}

// CreateRuleInput is the JSON shape the create_rule tool accepts. Rule is
// passed through to ittt-orchestrator unchanged (see internal/ittt).
type CreateRuleInput struct {
	Rule json.RawMessage `json:"rule"`
}

// UpdateRuleInput is the JSON shape the update_rule tool accepts.
type UpdateRuleInput struct {
	RuleID int             `json:"rule_id"`
	Rule   json.RawMessage `json:"rule"`
}

// ruleIDProperty is the shared rule_id input property of the rule tools.
var ruleIDProperty = map[string]any{
	"type":        "integer",
	"description": "The rule's numeric ID, from list_rules.",
}

// ToolDefinitions is the fixed set of tools offered to the LLM on every
// turn of every conversation, per the README's "forwards them to an LLM
// together with the available tools". Discovery (which devices/groups and
// capabilities exist) is itself a tool call (list_devices/list_groups)
// rather than being embedded in the system prompt, so the schema here does
// not need to change as devices come and go.
func ToolDefinitions() []anthropic.ToolUnionParam {
	tools := []anthropic.ToolUnionParam{
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
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolFuzzyFindGroup,
			Description: anthropic.String("Fuzzy-search for a group by name, returning the best-matching groups ordered from best to worst match, each with its numeric ID and a relevance score. Prefer this over list_groups when the user refers to a group by name rather than by ID, or when the name they gave might be misspelled, partial, or approximate -- it is a faster and more reliable way to identify which group they mean than listing every group and matching names yourself."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "The group name (or fragment of it) to search for, as given by the user. Does not need to be an exact match.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results to return. Defaults to 10 if omitted.",
					},
				},
				Required: []string{"query"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolListRules,
			Description: anthropic.String("List every automation rule: its numeric ID, name, whether it is enabled, its condition tree, its actions, and its next scheduled evaluation. Call this to find a rule the user refers to, or to see what automations already exist before creating a new one."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolGetRule,
			Description: anthropic.String("Get a single automation rule by its numeric ID, in full. Call this right before update_rule, so the replacement is based on the rule's current state."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{"rule_id": ruleIDProperty},
				Required:   []string{"rule_id"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolGetRuleSchema,
			Description: anthropic.String("Get the JSON Schema of an automation rule: every condition type and its fields, the action format, and descriptions of what each part does and when a rule fires. Call this before create_rule or update_rule unless you already have it in this conversation -- never guess a condition type or field name."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolCreateRule,
			Description: anthropic.String("Create a new automation rule: a condition tree that decides when it fires, and the device or group capabilities it triggers when it does. The rule takes effect immediately if enabled. Use get_rule_schema for the format, and list_devices/list_groups (or fuzzy_find_group) for the device/group IDs, attribute names and capability names it refers to. Returns the rule as stored, with its new ID."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"rule": map[string]any{
						"type":        "object",
						"description": "The rule, matching the schema returned by get_rule_schema. Omit id, next-occurence, cooldown-until and action-id; they are assigned by the system.",
					},
				},
				Required: []string{"rule"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolUpdateRule,
			Description: anthropic.String("Replace an existing automation rule wholesale: its name, enabled flag, entire condition tree and entire list of actions are all overwritten by what you send, and anything you leave out is removed. To change part of a rule (for example, only disable it or only change one time), call get_rule first and send the full rule back with just that part changed. The change takes effect immediately. Returns the rule as stored."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"rule_id": ruleIDProperty,
					"rule": map[string]any{
						"type":        "object",
						"description": "The complete replacement rule, matching the schema returned by get_rule_schema.",
					},
				},
				Required: []string{"rule_id", "rule"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolDeleteRule,
			Description: anthropic.String("Permanently delete an automation rule. This cannot be undone. Prefer disabling the rule (update_rule with enabled set to false) when the user only wants it to stop for a while."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{"rule_id": ruleIDProperty},
				Required:   []string{"rule_id"},
			},
		}},
		{OfTool: &anthropic.ToolParam{
			Name:        ToolEvaluateRule,
			Description: anthropic.String("Evaluate an automation rule's condition tree against the current device states and time, without triggering its actions. Returns whether it is currently true, the reason when it is false, and when it will next be evaluated on a schedule. Use this to explain why a rule did or did not fire, or to check a rule you just created or changed."),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: map[string]any{"rule_id": ruleIDProperty},
				Required:   []string{"rule_id"},
			},
		}},
	}
	// A cache_control breakpoint on the last tool definition covers the
	// whole (fixed, identical-every-call) tools array -- see
	// internal/llm/client.go's matching breakpoints on the system prompt and
	// the last message of history. Set here rather than in a literal so it
	// stays on the last tool whichever tool is appended next.
	tools[len(tools)-1].OfTool.CacheControl = anthropic.NewCacheControlEphemeralParam()
	return tools
}
