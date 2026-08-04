// Package restwebapp implements the REST + SSE API described in the
// README's "API" section on top of internal/conversation.Service.
package restwebapp

import (
	"context"
	"errors"
	"strings"

	"github.com/Kaese72/chatbot/internal/conversation"
	"github.com/Kaese72/chatbot/internal/identity"
	"github.com/Kaese72/chatbot/internal/persistence"
	"github.com/Kaese72/chatbot/restmodels"
	log "github.com/Kaese72/huemie-lib/logging"
	"github.com/danielgtaylor/huma/v2"
)

// WebApp holds the handlers for every /chatbot-service/v0/conversations...
// endpoint.
type WebApp struct {
	conversations *conversation.Service
	identity      *identity.Service
}

func NewWebApp(conversations *conversation.Service, identity *identity.Service) *WebApp {
	return &WebApp{conversations: conversations, identity: identity}
}

// mapServiceError translates the sentinel errors internal/persistence
// defines into the HTTP status codes the README's API section implies:
// unknown conversation -> 404, "another query can not be added to that
// conversation" / lock contention -> 409, no active API key configured ->
// 503 (the service cannot currently fulfill any request that talks to the
// LLM, but the request itself was well-formed).
func mapServiceError(err error) error {
	switch {
	case errors.Is(err, persistence.ErrConversationNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, persistence.ErrConversationNotAwaitingInput):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, persistence.ErrConversationLocked):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, persistence.ErrAPIKeyNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, persistence.ErrNoActiveAPIKey):
		return huma.Error503ServiceUnavailable("no active Anthropic API key is configured; add one via POST /chatbot-service/v0/api-keys and mark it active, or PATCH an existing key to active:true")
	default:
		log.Error(err.Error(), map[string]interface{}{})
		return huma.Error500InternalServerError("internal error")
	}
}

func (app *WebApp) ListConversations(ctx context.Context, input *struct{}) (*struct {
	Body restmodels.ConversationList
}, error) {
	conversations, err := app.conversations.ListConversations(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &struct {
		Body restmodels.ConversationList
	}{Body: restmodels.ConversationList{Conversations: conversations}}, nil
}

func (app *WebApp) NewConversation(ctx context.Context, input *struct {
	Body restmodels.NewConversationRequest
}) (*struct {
	Body restmodels.Conversation
}, error) {
	conv, err := app.conversations.New(ctx, input.Body.Query)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &struct {
		Body restmodels.Conversation
	}{Body: conv}, nil
}

func (app *WebApp) GetConversation(ctx context.Context, input *struct {
	ConversationID int64 `path:"conversationID"`
}) (*struct {
	Body restmodels.Conversation
}, error) {
	conv, err := app.conversations.GetConversation(ctx, input.ConversationID)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &struct {
		Body restmodels.Conversation
	}{Body: conv}, nil
}

func (app *WebApp) InputConversation(ctx context.Context, input *struct {
	ConversationID int64 `path:"conversationID"`
	Body           restmodels.InputRequest
}) (*struct {
	Body restmodels.Conversation
}, error) {
	conv, err := app.conversations.Input(ctx, input.ConversationID, input.Body.Query)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &struct {
		Body restmodels.Conversation
	}{Body: conv}, nil
}

func (app *WebApp) TerminateConversation(ctx context.Context, input *struct {
	ConversationID int64 `path:"conversationID"`
}) (*struct{}, error) {
	if err := app.conversations.Terminate(ctx, input.ConversationID); err != nil {
		return nil, mapServiceError(err)
	}
	return &struct{}{}, nil
}

func (app *WebApp) ForgetConversation(ctx context.Context, input *struct {
	ConversationID int64 `path:"conversationID"`
}) (*struct{}, error) {
	if err := app.conversations.Forget(ctx, input.ConversationID); err != nil {
		return nil, mapServiceError(err)
	}
	return &struct{}{}, nil
}

func (app *WebApp) ListAPIKeys(ctx context.Context, input *struct{}) (*struct {
	Body restmodels.APIKeyList
}, error) {
	keys, err := app.conversations.ListAPIKeys(ctx)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &struct {
		Body restmodels.APIKeyList
	}{Body: restmodels.APIKeyList{APIKeys: keys}}, nil
}

func (app *WebApp) CreateAPIKey(ctx context.Context, input *struct {
	Body restmodels.NewAPIKeyRequest
}) (*struct {
	Body restmodels.APIKey
}, error) {
	key, err := app.conversations.CreateAPIKey(ctx, input.Body)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &struct {
		Body restmodels.APIKey
	}{Body: key}, nil
}

func (app *WebApp) GetAPIKey(ctx context.Context, input *struct {
	APIKeyID int64 `path:"apiKeyID"`
}) (*struct {
	Body restmodels.APIKey
}, error) {
	key, err := app.conversations.GetAPIKey(ctx, input.APIKeyID)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &struct {
		Body restmodels.APIKey
	}{Body: key}, nil
}

func (app *WebApp) UpdateAPIKey(ctx context.Context, input *struct {
	APIKeyID int64 `path:"apiKeyID"`
	Body     restmodels.UpdateAPIKeyRequest
}) (*struct {
	Body restmodels.APIKey
}, error) {
	key, err := app.conversations.UpdateAPIKey(ctx, input.APIKeyID, input.Body)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &struct {
		Body restmodels.APIKey
	}{Body: key}, nil
}

func (app *WebApp) DeleteAPIKey(ctx context.Context, input *struct {
	APIKeyID int64 `path:"apiKeyID"`
}) (*struct{}, error) {
	if err := app.conversations.DeleteAPIKey(ctx, input.APIKeyID); err != nil {
		return nil, mapServiceError(err)
	}
	return &struct{}{}, nil
}

// SetupIdentity creates the chatbot's own user in the authentication
// service and saves it as the identity every subsequent tool call
// authenticates as. It reuses the caller's own bearer token (already
// validated by the router's UseTokenMiddleware) to authorize the
// authentication-service's POST /users call, so no additional privilege
// beyond "some authenticated user requested this" is required or checked.
func (app *WebApp) SetupIdentity(ctx context.Context, input *struct {
	Authorization string `header:"Authorization"`
	Body          restmodels.SetupIdentityRequest
}) (*struct{}, error) {
	token := strings.TrimSpace(strings.TrimPrefix(input.Authorization, "Bearer "))
	if token == "" {
		return nil, huma.Error401Unauthorized("missing bearer token")
	}
	if err := app.identity.Setup(ctx, token, input.Body.Name); err != nil {
		log.Error(err.Error(), map[string]interface{}{})
		return nil, huma.Error502BadGateway("failed to set up chatbot identity: " + err.Error())
	}
	return &struct{}{}, nil
}

// IdentityStatus reports whether POST /identities/setup has been completed,
// so a UI can decide whether to show onboarding, per the README's
// architecture note on this mechanism.
func (app *WebApp) IdentityStatus(ctx context.Context, input *struct{}) (*struct {
	Body restmodels.IdentityStatus
}, error) {
	configured, err := app.identity.Status(ctx)
	if err != nil {
		log.Error(err.Error(), map[string]interface{}{})
		return nil, huma.Error500InternalServerError("failed to query identity status")
	}
	return &struct {
		Body restmodels.IdentityStatus
	}{Body: restmodels.IdentityStatus{Configured: configured}}, nil
}
