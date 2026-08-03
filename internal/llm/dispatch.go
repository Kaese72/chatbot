package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Kaese72/chatbot/internal/devicestore"
)

// Dispatcher executes tool calls against device-store. Dispatch never
// returns a Go error for a malformed or failing tool call — a bad call is
// a normal outcome the LLM needs to see and can react to (per "Error
// handling in tool results": set is_error and describe what went wrong),
// not a service failure.
type Dispatcher struct {
	deviceStore *devicestore.Client
}

func NewDispatcher(deviceStore *devicestore.Client) *Dispatcher {
	return &Dispatcher{deviceStore: deviceStore}
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

	default:
		return false, fmt.Sprintf("unknown tool %q", name)
	}
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
