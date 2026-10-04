// Package restwebapp implements the REST + SSE API described in the
// README's "API" section on top of internal/conversation.Service.
package restwebapp

import (
	"context"
	"errors"

	"github.com/Kaese72/authentication/usertoken"
	"github.com/Kaese72/chatbot/internal/conversation"
	"github.com/Kaese72/chatbot/internal/persistence"
	"github.com/Kaese72/chatbot/restmodels"
	log "github.com/Kaese72/huemie-lib/logging"
	"github.com/Kaese72/huemie-lib/query"
	"github.com/danielgtaylor/huma/v2"
)

// WebApp holds the handlers for every /chatbot-service/v0/conversations...
// endpoint.
type WebApp struct {
	conversations *conversation.Service
}

func NewWebApp(conversations *conversation.Service) *WebApp {
	return &WebApp{conversations: conversations}
}

// callerID returns the authenticated user's ID, which the authentication
// service's usertoken.Middleware places in the request context. Every conversation handler needs
// it, since conversations are private to the user that started them.
func callerID(ctx context.Context) (int64, error) {
	id, ok := usertoken.UserID(ctx)
	if !ok {
		return 0, huma.Error401Unauthorized("missing or invalid bearer token")
	}
	return id, nil
}

// mapServiceError translates the sentinel errors internal/persistence
// defines into the HTTP status codes the README's API section implies:
// unknown conversation (or one owned by another user, deliberately
// indistinguishable so existence isn't leaked) -> 404, "another query can not be added to that
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

func (app *WebApp) ListConversations(ctx context.Context, input *struct {
	Filters string `query:"filters" doc:"a string JSON array of objects containing field, operator, and value for filtering"`
	Sort    string `query:"sort" doc:"a string JSON array of objects containing field and direction ('asc' or 'desc') for sorting"`
	query.Pagination
}) (*struct {
	query.TotalCount
	Body restmodels.ConversationList
}, error) {
	ownerID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	filters, err := query.ParseFilters(input.Filters)
	if err != nil {
		return nil, err
	}
	sorts, err := query.ParseSort(input.Sort)
	if err != nil {
		return nil, err
	}
	conversations, total, err := app.conversations.ListConversations(ctx, ownerID, filters, sorts, query.Pagination{Offset: input.Offset, Limit: input.Limit})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &struct {
		query.TotalCount
		Body restmodels.ConversationList
	}{
		TotalCount: query.TotalCount{TotalCount: total},
		Body:       restmodels.ConversationList{Conversations: conversations},
	}, nil
}

func (app *WebApp) NewConversation(ctx context.Context, input *struct {
	Body restmodels.NewConversationRequest
}) (*struct {
	Body restmodels.Conversation
}, error) {
	ownerID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := app.conversations.New(ctx, ownerID, input.Body.Query)
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
	ownerID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := app.conversations.GetConversation(ctx, ownerID, input.ConversationID)
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
	ownerID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	conv, err := app.conversations.Input(ctx, ownerID, input.ConversationID, input.Body.Query)
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
	ownerID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := app.conversations.Terminate(ctx, ownerID, input.ConversationID); err != nil {
		return nil, mapServiceError(err)
	}
	return &struct{}{}, nil
}

func (app *WebApp) ForgetConversation(ctx context.Context, input *struct {
	ConversationID int64 `path:"conversationID"`
}) (*struct{}, error) {
	ownerID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := app.conversations.Forget(ctx, ownerID, input.ConversationID); err != nil {
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

// GetStatus reports whether an active Anthropic API key is configured, so a
// UI has a single call to make before offering to start a new conversation.
func (app *WebApp) GetStatus(ctx context.Context, input *struct{}) (*struct {
	Body restmodels.ServiceStatus
}, error) {
	keys, err := app.conversations.ListAPIKeys(ctx)
	if err != nil {
		log.Error(err.Error(), map[string]interface{}{})
		return nil, huma.Error500InternalServerError("failed to query API key status")
	}
	hasActiveKey := false
	for _, key := range keys {
		if key.Type == restmodels.APIKeyTypeAnthropic && key.Active {
			hasActiveKey = true
			break
		}
	}
	return &struct {
		Body restmodels.ServiceStatus
	}{Body: restmodels.ServiceStatus{APIKey: hasActiveKey}}, nil
}
