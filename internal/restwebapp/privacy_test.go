package restwebapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kaese72/authentication/usertoken"
	"github.com/Kaese72/chatbot/internal/conversation"
	"github.com/Kaese72/chatbot/internal/events"
	"github.com/Kaese72/chatbot/internal/persistence"
	"github.com/Kaese72/chatbot/restmodels"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humamux"
	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/gorilla/mux"
)

// fakeDB owns exactly one conversation (ID 1, owner 100) and implements only
// the Persistence methods the routes under test touch; anything else panics
// via the nil embedded interface, which would itself flag an unexpected call.
type fakeDB struct {
	persistence.Persistence
	listedFor []int64
}

const (
	ownerA = int64(100)
	ownerB = int64(200)
)

func (f *fakeDB) ListConversations(_ context.Context, ownerID int64) ([]restmodels.Conversation, error) {
	f.listedFor = append(f.listedFor, ownerID)
	if ownerID != ownerA {
		return []restmodels.Conversation{}, nil
	}
	return []restmodels.Conversation{{ID: 1, Name: "secret"}}, nil
}

func (f *fakeDB) GetConversation(_ context.Context, ownerID int64, id int64) (restmodels.Conversation, error) {
	if id != 1 || ownerID != ownerA {
		return restmodels.Conversation{}, persistence.ErrConversationNotFound
	}
	return restmodels.Conversation{ID: 1, Name: "secret"}, nil
}

// ListDialogEntries returns one entry on the first read so an SSE stream
// actually sends its headers (they are only flushed on the first write);
// afterwards there is nothing new and the stream idles until the test's
// timeout cuts it off.
func (f *fakeDB) ListDialogEntries(_ context.Context, id int64, afterID int64) ([]restmodels.DialogEntry, error) {
	if afterID > 0 {
		return []restmodels.DialogEntry{}, nil
	}
	return []restmodels.DialogEntry{{ID: 1, ConversationID: id, Type: restmodels.DialogEntryTypeAgentInitiativeReleased}}, nil
}

func (f *fakeDB) ForgetConversation(_ context.Context, ownerID int64, id int64) error {
	if id != 1 || ownerID != ownerA {
		return persistence.ErrConversationNotFound
	}
	return nil
}

func newTestServer(t *testing.T) (*httptest.Server, func(userID int64) string, *fakeDB) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(userID int64) string {
		s, err := usertoken.Sign(key, userID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	db := &fakeDB{}
	svc := conversation.NewService(db, nil, nil, nil, events.NewRegistry(), events.NewRegistry(), 1)
	app := NewWebApp(svc, nil)

	router := mux.NewRouter()
	router.Use(usertoken.Middleware(&key.PublicKey, "/chatbot-service/openapi"))
	api := humamux.New(router, huma.DefaultConfig("test", "0"))
	huma.Get(api, "/chatbot-service/v0/conversations", app.ListConversations)
	huma.Get(api, "/chatbot-service/v0/conversations/{conversationID:[0-9]+}", app.GetConversation)
	huma.Post(api, "/chatbot-service/v0/conversations/{conversationID:[0-9]+}/forget", app.ForgetConversation)
	sse.Register(api, huma.Operation{
		OperationID: "follow-conversation",
		Method:      http.MethodGet,
		Path:        "/chatbot-service/v0/conversations/{conversationID:[0-9]+}/follow/{dialogEntryID:[0-9]+}",
		Middlewares: huma.Middlewares{app.RequireConversationOwner(api)},
	}, map[string]any{"dialog-entry": restmodels.DialogEntry{}}, app.FollowConversation)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, sign, db
}

func do(t *testing.T, method, url, token string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&body) // an open SSE stream is cut off by the timeout; only the status matters there
	return resp.StatusCode, string(body)
}

func TestConversationsArePrivateToTheirOwner(t *testing.T) {
	srv, sign, db := newTestServer(t)
	base := srv.URL + "/chatbot-service/v0/conversations"

	cases := []struct {
		name   string
		method string
		path   string
		token  string
		want   int
	}{
		{"owner can get", http.MethodGet, "/1", sign(ownerA), http.StatusOK},
		{"other user cannot get", http.MethodGet, "/1", sign(ownerB), http.StatusNotFound},
		{"other user cannot forget", http.MethodPost, "/1/forget", sign(ownerB), http.StatusNotFound},
		{"owner can forget", http.MethodPost, "/1/forget", sign(ownerA), http.StatusNoContent},
		{"owner can follow", http.MethodGet, "/1/follow/0", sign(ownerA), http.StatusOK},
		{"other user cannot follow", http.MethodGet, "/1/follow/0", sign(ownerB), http.StatusNotFound},
		{"unauthenticated cannot get", http.MethodGet, "/1", "", http.StatusUnauthorized},
		{"unauthenticated cannot follow", http.MethodGet, "/1/follow/0", "", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, body := do(t, tc.method, base+tc.path, tc.token); got != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", got, tc.want, body)
			}
		})
	}

	t.Run("list is scoped to the caller", func(t *testing.T) {
		if _, body := do(t, http.MethodGet, base, sign(ownerB)); body != `{"conversations":[]}` && !containsEmptyList(body) {
			t.Fatalf("user B saw conversations: %s", body)
		}
		if _, body := do(t, http.MethodGet, base, sign(ownerA)); !containsSecret(body) {
			t.Fatalf("owner should see their conversation, got %s", body)
		}
		for _, id := range db.listedFor {
			if id != ownerA && id != ownerB {
				t.Fatalf("list queried for unexpected owner %d", id)
			}
		}
	})
}

func containsEmptyList(body string) bool {
	var v struct {
		Conversations []restmodels.Conversation `json:"conversations"`
	}
	return json.Unmarshal([]byte(body), &v) == nil && len(v.Conversations) == 0
}

func containsSecret(body string) bool {
	var v struct {
		Conversations []restmodels.Conversation `json:"conversations"`
	}
	return json.Unmarshal([]byte(body), &v) == nil && len(v.Conversations) == 1 && v.Conversations[0].Name == "secret"
}
