package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kaese72/authentication/usertoken"
	"github.com/gorilla/mux"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// stubIntrospector accepts exactly one API token, for user 7.
type stubIntrospector struct{ token string }

func (s stubIntrospector) Introspect(_ context.Context, token string) (int64, usertoken.Permissions, error) {
	if token != s.token {
		return 0, usertoken.Permissions{}, usertoken.ErrInvalidAPIToken
	}
	return 7, usertoken.Permissions{}, nil
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// fakeDeviceStore records the Authorization header of every request and
// answers the list/trigger endpoints the dispatcher uses.
type fakeDeviceStore struct {
	mu      sync.Mutex
	bearers []string
	paths   []string
}

func (f *fakeDeviceStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.bearers = append(f.bearers, r.Header.Get("Authorization"))
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/device-store/v0/devices":
		io.WriteString(w, `[{"id":1}]`)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/device-store/v0/devices/1/capabilities/"):
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func setup(t *testing.T) (*httptest.Server, *fakeDeviceStore, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ds := &fakeDeviceStore{}
	dsServer := httptest.NewServer(ds)
	t.Cleanup(dsServer.Close)

	apiToken, _, _, err := usertoken.GenerateAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	// Same middleware options as main.go.
	router.Use(usertoken.NewMiddleware(&key.PublicKey,
		usertoken.SkipPrefixes("/chatbot-service/openapi", "/chatbot-service/docs"),
		usertoken.AcceptAPITokens(stubIntrospector{token: apiToken}),
		usertoken.RejectAPITokensFor("/chatbot-service/v0/"),
	))
	router.Handle(Path, NewHandler(dsServer.URL, "test"))
	router.HandleFunc("/chatbot-service/v0/conversations", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, ds, key, apiToken
}

func connect(t *testing.T, srv *httptest.Server, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + Path,
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
	}, nil)
}

func TestToolsForwardCallerToken(t *testing.T) {
	srv, ds, key, apiToken := setup(t)
	useToken, err := usertoken.Sign(key, 3, time.Minute, usertoken.Permissions{})
	if err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{"use token": useToken, "API token": apiToken} {
		t.Run(name, func(t *testing.T) {
			session, err := connect(t, srv, token)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer session.Close()
			ctx := context.Background()

			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatalf("list tools: %v", err)
			}
			if len(tools.Tools) != 5 {
				t.Fatalf("want 5 tools, got %d", len(tools.Tools))
			}

			res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_devices", Arguments: map[string]any{}})
			if err != nil || res.IsError {
				t.Fatalf("list_devices: err=%v result=%+v", err, res)
			}
			if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"id":1`) {
				t.Fatalf("unexpected list_devices output: %s", text)
			}

			res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "trigger_device_capability", Arguments: map[string]any{"device_id": 1, "capability": "activate"}})
			if err != nil || res.IsError {
				t.Fatalf("trigger: err=%v result=%+v", err, res)
			}

			// Device-store failures come back as tool errors, not protocol errors.
			res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "trigger_device_capability", Arguments: map[string]any{"device_id": 2, "capability": "activate"}})
			if err != nil || !res.IsError {
				t.Fatalf("want tool error for unknown device: err=%v result=%+v", err, res)
			}

			ds.mu.Lock()
			defer ds.mu.Unlock()
			for i, b := range ds.bearers {
				if b != "Bearer "+token {
					t.Fatalf("device-store request %q got %q, want the caller's own token", ds.paths[i], b)
				}
			}
			ds.bearers, ds.paths = nil, nil
		})
	}
}

func TestAuthentication(t *testing.T) {
	srv, _, key, apiToken := setup(t)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged, _ := usertoken.Sign(otherKey, 3, time.Minute, usertoken.Permissions{})

	for name, token := range map[string]string{
		"no token":        "",
		"forged token":    forged,
		"unknown API key": "humi_pat_0000000000000000_" + strings.Repeat("0", 64),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := connect(t, srv, token); err == nil {
				t.Fatal("connect succeeded, want refusal")
			}
		})
	}

	// API tokens stay refused on the conversation/api-key routes.
	status := func(token string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/chatbot-service/v0/conversations", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := status(apiToken); got != http.StatusForbidden {
		t.Fatalf("API token on /v0/: got %d, want 403", got)
	}
	useToken, _ := usertoken.Sign(key, 3, time.Minute, usertoken.Permissions{})
	if got := status(useToken); got != http.StatusOK {
		t.Fatalf("use token on /v0/: got %d, want 200", got)
	}
}
