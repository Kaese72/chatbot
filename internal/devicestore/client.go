// Package devicestore is an HTTP client for the device-store service's
// public API, used to enumerate the devices/groups and capabilities
// available to the LLM as tools, and to trigger them. It authenticates as
// the bot's own identity (Bearer use-token, fetched fresh for every request
// via the tokenProvider passed to NewClient -- see internal/identity), per
// the README's requirement that "the bot is authenticated as its own user,
// to the system, and makes actions via that user."
//
// Response types are device-store's own restmodels, imported directly
// rather than mirrored locally -- restmodels has no dependencies beyond the
// standard library (see github.com/Kaese72/device-store/restmodels), so
// depending on it doesn't pull device-store's heavier transitive deps (its
// DB driver, APM modules, etc.) into this binary; go only compiles and
// links packages actually imported. This also means a field device-store
// adds or renames can't silently drift out of what the chatbot sees, unlike
// a hand-maintained mirror struct would.
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

	"github.com/Kaese72/device-store/restmodels"
)

// pageLimit is the maximum page size device-store's public API accepts for
// list endpoints (see device-store's GetDevices/GetGroups query binding).
const pageLimit = 200

// CapabilityArgs is the free-form argument object passed through to a
// device or group capability trigger.
type CapabilityArgs map[string]any

// Client is an HTTP client for device-store's public API.
type Client struct {
	baseURL       string
	tokenProvider func(context.Context) (string, error)
	httpClient    *http.Client
}

// NewClient builds a device-store client that calls tokenProvider to obtain
// a bearer token immediately before every request, rather than holding one
// fixed at construction time -- see internal/identity.Service.DeviceStoreToken,
// the only tokenProvider this is actually constructed with.
func NewClient(baseURL string, tokenProvider func(context.Context) (string, error)) *Client {
	return &Client{
		baseURL:       baseURL,
		tokenProvider: tokenProvider,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ListDevices returns every device known to device-store, paging through
// the public API's list endpoint (capped at 200 per page) rather than
// silently truncating to the first page.
func (c *Client) ListDevices(ctx context.Context) ([]restmodels.Device, error) {
	all := []restmodels.Device{}
	offset := 0
	for {
		var page []restmodels.Device
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
func (c *Client) ListGroups(ctx context.Context) ([]restmodels.Group, error) {
	all := []restmodels.Group{}
	offset := 0
	for {
		var page []restmodels.Group
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

// SearchGroups fuzzy-searches device-store for groups by name, returning up
// to limit results ordered from best to worst match.
func (c *Client) SearchGroups(ctx context.Context, query string, limit int) ([]restmodels.GroupSearchResult, error) {
	path := fmt.Sprintf("/device-store/v0/groups/search?q=%s&limit=%d", url.QueryEscape(query), limit)
	results := []restmodels.GroupSearchResult{}
	if err := c.getJSON(ctx, path, &results); err != nil {
		return nil, err
	}
	return results, nil
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
	token, err := c.tokenProvider(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
	token, err := c.tokenProvider(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
