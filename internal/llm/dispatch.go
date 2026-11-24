package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Kaese72/chatbot/internal/devicestore"
	"github.com/Kaese72/chatbot/internal/ittt"
)

// Dispatcher executes tool calls against device-store and
// ittt-orchestrator. Dispatch never
// returns a Go error for a malformed or failing tool call — a bad call is
// a normal outcome the LLM needs to see and can react to (per "Error
// handling in tool results": set is_error and describe what went wrong),
// not a service failure.
type Dispatcher struct {
	deviceStore *devicestore.Client
	rules       *ittt.Client
}

// NewDispatcher builds a Dispatcher. Both clients must authenticate as the
// same identity (the conversation owner, or the MCP caller), so device and
// rule tools always act with the same permissions.
func NewDispatcher(deviceStore *devicestore.Client, rules *ittt.Client) *Dispatcher {
	return &Dispatcher{deviceStore: deviceStore, rules: rules}
}

// Dispatch runs the named tool with the given raw JSON input and reports
// whether it succeeded, plus the text to send back to the LLM as the
// tool_result content.
func (d *Dispatcher) Dispatch(ctx context.Context, name string, input json.RawMessage) (success bool, resultText string) {
	switch name {
	case ToolListDevices:
		devices, err := d.deviceStore.ListDevices(ctx)
		if err != nil {
			return false, fmt.Sprintf("failed to list devices: %s", err.Error())
		}
		body, err := json.Marshal(devices)
		if err != nil {
			return false, fmt.Sprintf("failed to encode devices: %s", err.Error())
		}
		return true, string(body)

	case ToolListGroups:
		groups, err := d.deviceStore.ListGroups(ctx)
		if err != nil {
			return false, fmt.Sprintf("failed to list groups: %s", err.Error())
		}
		body, err := json.Marshal(groups)
		if err != nil {
			return false, fmt.Sprintf("failed to encode groups: %s", err.Error())
		}
		return true, string(body)

	case ToolFuzzyFindGroup:
		var in FuzzyFindGroupInput
		if err := json.Unmarshal(input, &in); err != nil {
			return false, fmt.Sprintf("invalid tool input: %s", err.Error())
		}
		if in.Query == "" {
			return false, "query must not be empty"
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}
		results, err := d.deviceStore.SearchGroups(ctx, in.Query, limit)
		if err != nil {
			return false, fmt.Sprintf("failed to search groups: %s", err.Error())
		}
		body, err := json.Marshal(results)
		if err != nil {
			return false, fmt.Sprintf("failed to encode search results: %s", err.Error())
		}
		return true, string(body)

	case ToolTriggerDeviceCapability:
		var in DeviceCapabilityTriggerInput
		if err := json.Unmarshal(input, &in); err != nil {
			return false, fmt.Sprintf("invalid tool input: %s", err.Error())
		}
		args, err := decodeArgs(in.Args)
		if err != nil {
			return false, fmt.Sprintf("invalid args: %s", err.Error())
		}
		if err := d.deviceStore.TriggerDeviceCapability(ctx, in.DeviceID, in.Capability, args); err != nil {
			return false, err.Error()
		}
		return true, "triggered"

	case ToolTriggerGroupCapability:
		var in GroupCapabilityTriggerInput
		if err := json.Unmarshal(input, &in); err != nil {
			return false, fmt.Sprintf("invalid tool input: %s", err.Error())
		}
		args, err := decodeArgs(in.Args)
		if err != nil {
			return false, fmt.Sprintf("invalid args: %s", err.Error())
		}
		if err := d.deviceStore.TriggerGroupCapability(ctx, in.GroupID, in.Capability, args); err != nil {
			return false, err.Error()
		}
		return true, "triggered"

	case ToolListRules:
		rules, err := d.rules.ListRules(ctx)
		if err != nil {
			return false, fmt.Sprintf("failed to list rules: %s", err.Error())
		}
		return encodeResult(rules)

	case ToolGetRuleSchema:
		// Relayed as ittt-orchestrator serves it: the schema and its
		// descriptions are maintained there, next to the rule types.
		schema, err := d.rules.RuleSchema(ctx)
		if err != nil {
			return false, fmt.Sprintf("failed to get rule schema: %s", err.Error())
		}
		return true, string(schema)

	case ToolGetRule, ToolDeleteRule, ToolEvaluateRule:
		var in RuleIDInput
		if err := json.Unmarshal(input, &in); err != nil {
			return false, fmt.Sprintf("invalid tool input: %s", err.Error())
		}
		if in.RuleID <= 0 {
			return false, "rule_id must be a positive rule ID"
		}
		switch name {
		case ToolGetRule:
			rule, err := d.rules.GetRule(ctx, in.RuleID)
			if err != nil {
				return false, err.Error()
			}
			return true, string(rule)
		case ToolDeleteRule:
			if err := d.rules.DeleteRule(ctx, in.RuleID); err != nil {
				return false, err.Error()
			}
			return true, "deleted"
		default:
			result, err := d.rules.EvaluateRule(ctx, in.RuleID)
			if err != nil {
				return false, err.Error()
			}
			return true, string(result)
		}

	case ToolCreateRule:
		var in CreateRuleInput
		if err := json.Unmarshal(input, &in); err != nil {
			return false, fmt.Sprintf("invalid tool input: %s", err.Error())
		}
		created, err := d.rules.CreateRule(ctx, in.Rule)
		if err != nil {
			return false, err.Error()
		}
		return true, string(created)

	case ToolUpdateRule:
		var in UpdateRuleInput
		if err := json.Unmarshal(input, &in); err != nil {
			return false, fmt.Sprintf("invalid tool input: %s", err.Error())
		}
		if in.RuleID <= 0 {
			return false, "rule_id must be a positive rule ID"
		}
		updated, err := d.rules.UpdateRule(ctx, in.RuleID, in.Rule)
		if err != nil {
			return false, err.Error()
		}
		return true, string(updated)

	default:
		return false, fmt.Sprintf("unknown tool %q", name)
	}
}

func encodeResult(v any) (bool, string) {
	body, err := json.Marshal(v)
	if err != nil {
		return false, fmt.Sprintf("failed to encode result: %s", err.Error())
	}
	return true, string(body)
}

func decodeArgs(raw json.RawMessage) (devicestore.CapabilityArgs, error) {
	if len(raw) == 0 {
		return devicestore.CapabilityArgs{}, nil
	}
	var args devicestore.CapabilityArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	return args, nil
}
