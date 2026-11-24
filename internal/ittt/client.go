// Package ittt is an HTTP client for ittt-orchestrator's REST API (api mode),
// used by the rule tools to list, read, create, replace, delete and evaluate
// automation rules.
//
// Like internal/devicestore, it authenticates with whatever bearer token the
// tokenProvider passed to NewClient returns, fetched fresh before every
// request: the conversation owner's impersonation token for the chatbot's
// own LLM (internal/serviceauth), or the MCP caller's own forwarded token
// (internal/mcpserver). ittt-orchestrator then enforces that identity's own
// automation-rule view/modify permissions.
//
// Rule bodies are passed through as raw JSON rather than decoded into
// ittt-orchestrator's restmodels.Rule: the deployed ittt-orchestrator can be
// newer than the version this module pins (for example, a condition type
// added there), and decoding into an older Go type would reject or silently
// drop what it doesn't know. ittt-orchestrator validates every write itself.
package ittt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// pageLimit is the maximum page size ittt-orchestrator's list endpoint
// accepts (huemie-lib/query.Pagination).
const pageLimit = 200

const rulesPath = "/ittt-orchestrator/v0/rules"

// Client is an HTTP client for ittt-orchestrator's REST API.
type Client struct {
	baseURL       string
	tokenProvider func(context.Context) (string, error)
	httpClient    *http.Client
}

// NewClient builds an ittt-orchestrator client that calls tokenProvider for a
// bearer token immediately before every request.
func NewClient(baseURL string, tokenProvider func(context.Context) (string, error)) *Client {
	return &Client{
		baseURL:       baseURL,
		tokenProvider: tokenProvider,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// ListRules returns every rule, paging through the list endpoint rather than
// silently truncating to the first page.
func (c *Client) ListRules(ctx context.Context) ([]json.RawMessage, error) {
	all := []json.RawMessage{}
	offset := 0
	for {
		var page []json.RawMessage
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s?offset=%d&limit=%d", rulesPath, offset, pageLimit), nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageLimit {
			return all, nil
		}
		offset += pageLimit
	}
}

// GetRule returns a single rule.
func (c *Client) GetRule(ctx context.Context, ruleID int) (json.RawMessage, error) {
	var rule json.RawMessage
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d", rulesPath, ruleID), nil, &rule)
	return rule, err
}

// CreateRule creates a rule (condition tree and actions included) and
// returns it as stored.
func (c *Client) CreateRule(ctx context.Context, rule json.RawMessage) (json.RawMessage, error) {
	var created json.RawMessage
	err := c.do(ctx, http.MethodPost, rulesPath, rule, &created)
	return created, err
}

// UpdateRule replaces a rule wholesale (condition tree and actions included)
// and returns it as stored.
func (c *Client) UpdateRule(ctx context.Context, ruleID int, rule json.RawMessage) (json.RawMessage, error) {
	var updated json.RawMessage
	err := c.do(ctx, http.MethodPut, fmt.Sprintf("%s/%d", rulesPath, ruleID), rule, &updated)
	return updated, err
}

// DeleteRule deletes a rule.
func (c *Client) DeleteRule(ctx context.Context, ruleID int) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", rulesPath, ruleID), nil, nil)
}

// EvaluateRule evaluates a stored rule's condition tree against the current
// state, without triggering its actions, and returns ittt-orchestrator's
// result (result, reason, next-occurrence).
func (c *Client) EvaluateRule(ctx context.Context, ruleID int) (json.RawMessage, error) {
	var result json.RawMessage
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d/evaluate", rulesPath, ruleID), nil, &result)
	return result, err
}

// RuleSchema returns the rule JSON Schema the deployed ittt-orchestrator
// validates against, with its descriptions of how each part behaves (see
// ittt-orchestrator's GET /ittt-orchestrator/v0/rules/schema). Fetched live
// rather than built into this binary, so a new condition type reaches the
// chatbot's LLM and MCP clients as soon as ittt-orchestrator is deployed.
func (c *Client) RuleSchema(ctx context.Context) (json.RawMessage, error) {
	var schema json.RawMessage
	err := c.do(ctx, http.MethodGet, rulesPath+"/schema", nil, &schema)
	return schema, err
}

// do sends one request. A non-2xx response becomes an error carrying
// ittt-orchestrator's response body, which for a validation failure is the
// huma problem document explaining what is wrong with the rule -- exactly
// what the model needs to correct it.
func (c *Client) do(ctx context.Context, method, path string, body json.RawMessage, out any) error {
	token, err := c.tokenProvider(ctx)
	if err != nil {
		return err
	}
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ittt-orchestrator returned %d for %s %s: %s", resp.StatusCode, method, path, respBody)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
