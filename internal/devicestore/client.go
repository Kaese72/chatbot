// Package devicestore is an HTTP client for the device-store service's
// public API, used to enumerate the devices/groups and capabilities
// available to the LLM as tools, and to trigger them. It authenticates as
// the bot's own configured user (Bearer JWT), per the README's requirement
// that "the bot is authenticated as its own user, to the system, and makes
// actions via that user."
//
// The types below intentionally mirror only the subset of device-store's
// restmodels package the chatbot needs, defined locally rather than
// imported from the device-store module — the same convention
// ittt-orchestrator's internal/devicestore client already follows in this
// monorepo, which avoids pulling device-store's full dependency tree into
// an unrelated service for the sake of a handful of struct shapes.
package devicestore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// pageLimit is the maximum page size device-store's public API accepts for
// list endpoints (see device-store's GetDevices/GetGroups query binding).
const pageLimit = 200

// BooleanArgumentSpec, NumericArgumentSpec, TextArgumentSpec, and
// ArgumentSpec mirror device-store's restmodels/capabilityschema.go. They
// describe the shape of arguments a capability accepts, which the chatbot
// translates into the trigger_device_capability / trigger_group_capability
// tool's JSON schema for the LLM.
type BooleanArgumentSpec struct {
	Default *bool `json:"default,omitempty"`
}

type NumericArgumentSpec struct {
	Min     float32  `json:"min"`
	Max     float32  `json:"max"`
	Default *float32 `json:"default,omitempty"`
}

type TextArgumentSpec struct {
	Default *string `json:"default,omitempty"`
	Min     *int    `json:"min,omitempty"`
	Max     *int    `json:"max,omitempty"`
}

type ArgumentSpec struct {
	Name    string               `json:"name"`
	Boolean *BooleanArgumentSpec `json:"boolean,omitempty"`
	Numeric *NumericArgumentSpec `json:"numeric,omitempty"`
	Text    *TextArgumentSpec    `json:"text,omitempty"`
}

type Attribute struct {
	Name    string   `json:"name"`
	Boolean *bool    `json:"boolean-state,omitempty"`
	Numeric *float32 `json:"numeric-state,omitempty"`
	Text    *string  `json:"string-state,omitempty"`
}

type DeviceCapability struct {
	Name          string         `json:"name"`
	ArgumentSpecs []ArgumentSpec `json:"argument-specs"`
	Updated       time.Time      `json:"updated"`
}

type GroupCapability struct {
	Name          string         `json:"name"`
	ArgumentSpecs []ArgumentSpec `json:"argument-specs"`
	Updated       time.Time      `json:"updated"`
}

type Device struct {
	ID               int                `json:"id"`
	BridgeIdentifier string             `json:"bridge-identifier"`
	AdapterId        int                `json:"adapter-id"`
	Updated          time.Time          `json:"updated"`
	Attributes       []Attribute        `json:"attributes"`
	Capabilities     []DeviceCapability `json:"capabilities"`
	GroupIds         []int              `json:"group-ids"`
}

type Group struct {
	ID               int               `json:"id"`
	Name             string            `json:"name"`
	AdapterId        int               `json:"adapter-id"`
	BridgeIdentifier string            `json:"bridge-identifier"`
	Updated          time.Time         `json:"updated"`
	Capabilities     []GroupCapability `json:"capabilities"`
	DeviceIds        []int             `json:"device-ids"`
}

// CapabilityArgs is the free-form argument object passed through to a
// device or group capability trigger.
type CapabilityArgs map[string]any

// Client is an HTTP client for device-store's public API.
type Client struct {
	baseURL    string
	jwt        string
	httpClient *http.Client
}

func NewClient(baseURL string, jwt string) *Client {
	return &Client{
		baseURL:    baseURL,
		jwt:        jwt,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ListDevices returns every device known to device-store, paging through
// the public API's list endpoint (capped at 200 per page) rather than
// silently truncating to the first page.
func (c *Client) ListDevices(ctx context.Context) ([]Device, error) {
	all := []Device{}
	offset := 0
	for {
		var page []Device
		if err := c.getJSON(ctx, fmt.Sprintf("/device-store/v0/devices?offset=%d&limit=%d", offset, pageLimit), &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageLimit {
			return all, nil
		}
		offset += pageLimit
	}
}

// ListGroups returns every group known to device-store, paging through the
// public API's list endpoint (capped at 200 per page).
func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	all := []Group{}
	offset := 0
	for {
		var page []Group
		if err := c.getJSON(ctx, fmt.Sprintf("/device-store/v0/groups?offset=%d&limit=%d", offset, pageLimit), &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageLimit {
			return all, nil
		}
		offset += pageLimit
	}
}

// TriggerDeviceCapability invokes a device capability trigger.
func (c *Client) TriggerDeviceCapability(ctx context.Context, deviceID int, capability string, args CapabilityArgs) error {
	path := fmt.Sprintf("/device-store/v0/devices/%d/capabilities/%s", deviceID, url.PathEscape(capability))
	return c.postJSON(ctx, path, args)
}

// TriggerGroupCapability invokes a group capability trigger.
func (c *Client) TriggerGroupCapability(ctx context.Context, groupID int, capability string, args CapabilityArgs) error {
	path := fmt.Sprintf("/device-store/v0/groups/%d/capabilities/%s", groupID, url.PathEscape(capability))
	return c.postJSON(ctx, path, args)
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.jwt)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("device-store returned %d for GET %s: %s", resp.StatusCode, path, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) postJSON(ctx context.Context, path string, args CapabilityArgs) error {
	if args == nil {
		args = CapabilityArgs{}
	}
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.jwt)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("device-store returned %d for POST %s: %s", resp.StatusCode, path, respBody)
	}
	return nil
}
