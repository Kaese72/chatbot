// Package authclient is an HTTP client for the authentication service's
// internal API, used by internal/serviceauth to exchange this pod's own
// Kubernetes ServiceAccount token for a short-lived use-token impersonating
// whichever user is driving the current conversation turn.
package authclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is an HTTP client for the authentication service's internal API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type impersonateResponse struct {
	UseToken string `json:"use-token"`
}

// Impersonate calls POST /authentication-service/v0/internal/impersonate/{id}
// on authentication's internal listener, presenting serviceToken -- this
// pod's own, audience-bound Kubernetes ServiceAccount token -- as the
// bearer credential. Returns a short-lived use-token for targetUserID,
// carrying that user's own real permissions, not the caller's.
func (c *Client) Impersonate(ctx context.Context, serviceToken string, targetUserID int64) (string, error) {
	path := fmt.Sprintf("%s/authentication-service/v0/internal/impersonate/%d", c.baseURL, targetUserID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+serviceToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("authentication service returned %d for POST /internal/impersonate/%d: %s", resp.StatusCode, targetUserID, respBody)
	}
	var out impersonateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.UseToken, nil
}
