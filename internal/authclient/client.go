// Package authclient is an HTTP client for the authentication service's own
// REST API, used by internal/identity for exactly two calls: creating the
// chatbot's own user (on behalf of whichever caller's use-token authorized
// the setup request) and logging in as that user fresh before every tool
// call that needs to act on another service.
package authclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is an HTTP client for the authentication service's public API.
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

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Surname  string `json:"surname"`
}

type createUserResponse struct {
	ID int64 `json:"id"`
}

// CreateUser calls POST /authentication-service/v0/users as callerToken --
// the use-token of whoever authorized the chatbot's POST /identities/setup
// call -- since that endpoint requires a valid bearer token and performs no
// authorization check of its own beyond that.
func (c *Client) CreateUser(ctx context.Context, callerToken string, username string, password string, name string) (int64, error) {
	body, err := json.Marshal(createUserRequest{Username: username, Password: password, Name: name, Surname: ""})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/authentication-service/v0/users", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+callerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("authentication service returned %d for POST /users: %s", resp.StatusCode, respBody)
	}
	var created createUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	UseToken string `json:"use-token"`
}

// Login calls POST /authentication-service/v0/authentication/login with a
// username/password (not the refresh-cookie flow -- the chatbot has no
// browser session to keep a cookie in), returning a fresh use-token.
func (c *Client) Login(ctx context.Context, username string, password string) (string, error) {
	body, err := json.Marshal(loginRequest{Username: username, Password: password})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/authentication-service/v0/authentication/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("authentication service returned %d for POST /authentication/login: %s", resp.StatusCode, respBody)
	}
	var login loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&login); err != nil {
		return "", err
	}
	return login.UseToken, nil
}
