// Package conversation implements the README's Architecture section: the
// per-replica locking protocol, the LLM turn/tool-call loop, and
// termination handling.
package conversation

import (
	"context"
	"errors"

	"github.com/Kaese72/chatbot/eventmodels"
	"github.com/Kaese72/chatbot/internal/events"
	"github.com/Kaese72/chatbot/internal/llm"
	"github.com/Kaese72/chatbot/internal/persistence"
	"github.com/Kaese72/chatbot/restmodels"
	log "github.com/Kaese72/huemie-lib/logging"
	"github.com/google/uuid"
)

// Service is the single entry point for everything conversation-related:
// the REST handlers in internal/restwebapp call it directly, and it
// implements events.Handler to react to RabbitMQ-delivered
// ConversationEvents from any replica.
type Service struct {
	db         persistence.Persistence
	llmClient  *llm.Client
	dispatcher *llm.Dispatcher
	publisher  *events.Publisher

	// terminations has at most one local subscriber per conversation ID:
	// the goroutine (if any) currently processing that conversation on
	// this replica.
	terminations *events.Registry
	// updates may have any number of local subscribers per conversation
	// ID: every open .../follow/{id} SSE connection served by this
	// replica.
	updates *events.Registry

	maxToolLoopIterations int
}

func NewService(
	db persistence.Persistence,
	llmClient *llm.Client,
	dispatcher *llm.Dispatcher,
	publisher *events.Publisher,
	terminations *events.Registry,
	updates *events.Registry,
	maxToolLoopIterations int,
) *Service {
	return &Service{
		db:                    db,
		llmClient:             llmClient,
		dispatcher:            dispatcher,
		publisher:             publisher,
		terminations:          terminations,
		updates:               updates,
		maxToolLoopIterations: maxToolLoopIterations,
	}
}

func (s *Service) ListConversations(ctx context.Context) ([]restmodels.Conversation, error) {
	return s.db.ListConversations(ctx)
}

func (s *Service) GetConversation(ctx context.Context, conversationID int64) (restmodels.Conversation, error) {
	return s.db.GetConversation(ctx, conversationID)
}

func (s *Service) ListDialogEntries(ctx context.Context, conversationID int64, afterID int64) ([]restmodels.DialogEntry, error) {
	return s.db.ListDialogEntries(ctx, conversationID, afterID)
}

// New starts a brand new conversation with an initial query. Processing
// happens on a detached background goroutine so the HTTP request returns
// immediately with the conversation in AGENT_IN_PROGRESS status; callers
// watch progress via .../follow/{id}.
func (s *Service) New(ctx context.Context, query string) (restmodels.Conversation, error) {
	// Fetched up front, before anything is written to the database: per
	// the README's Configuration section, any endpoint that would result
	// in new traffic to the LLM must fail fast with a presentable error if
	// no active key is configured, rather than creating a conversation
	// that can never make progress.
	apiKey, err := s.db.ActiveAPIKeyValue(ctx, restmodels.APIKeyTypeAnthropic)
	if err != nil {
		return restmodels.Conversation{}, err
	}
	lockID := uuid.NewString()
	conv, err := s.db.BeginConversation(ctx, lockID, query)
	if err != nil {
		return restmodels.Conversation{}, err
	}
	go s.process(context.Background(), conv.ID, lockID, apiKey)
	s.publishUpdated(conv.ID)
	return conv, nil
}

// Input submits a follow-up query to an existing conversation. The caller
// must currently hold the initiative (see persistence.ErrConversationNotAwaitingInput
// / persistence.ErrConversationLocked).
func (s *Service) Input(ctx context.Context, conversationID int64, query string) (restmodels.Conversation, error) {
	// See the identical check in New: fail fast rather than flip the
	// conversation to AGENT_IN_PROGRESS with no way to actually process it.
	apiKey, err := s.db.ActiveAPIKeyValue(ctx, restmodels.APIKeyTypeAnthropic)
	if err != nil {
		return restmodels.Conversation{}, err
	}
	lockID := uuid.NewString()
	if err := s.db.BeginInput(ctx, conversationID, lockID, query); err != nil {
		return restmodels.Conversation{}, err
	}
	go s.process(context.Background(), conversationID, lockID, apiKey)
	s.publishUpdated(conversationID)
	return s.db.GetConversation(ctx, conversationID)
}

// Terminate requests that any agent deliberation currently in progress for
// conversationID stop as soon as possible. It publishes a RabbitMQ event
// that every replica receives; only the replica (if any) actually
// processing the conversation will act on it.
func (s *Service) Terminate(ctx context.Context, conversationID int64) error {
	if _, err := s.db.GetConversation(ctx, conversationID); err != nil {
		return err
	}
	return s.publisher.Publish(eventmodels.ConversationEvent{
		ConversationID: conversationID,
		Event:          eventmodels.ConversationEventTerminate,
	})
}

// Forget permanently deletes a conversation and its DialogEntries. Only
// permitted while the user holds the initiative.
func (s *Service) Forget(ctx context.Context, conversationID int64) error {
	return s.db.ForgetConversation(ctx, conversationID)
}

// ListAPIKeys returns every configured APIKey. The secret value is never
// included.
func (s *Service) ListAPIKeys(ctx context.Context) ([]restmodels.APIKey, error) {
	return s.db.ListAPIKeys(ctx)
}

// GetAPIKey returns a single APIKey by ID. The secret value is never
// included.
func (s *Service) GetAPIKey(ctx context.Context, id int64) (restmodels.APIKey, error) {
	return s.db.GetAPIKey(ctx, id)
}

// CreateAPIKey stores a new APIKey.
func (s *Service) CreateAPIKey(ctx context.Context, req restmodels.NewAPIKeyRequest) (restmodels.APIKey, error) {
	return s.db.CreateAPIKey(ctx, req)
}

// UpdateAPIKey applies a partial update to an existing APIKey.
func (s *Service) UpdateAPIKey(ctx context.Context, id int64, req restmodels.UpdateAPIKeyRequest) (restmodels.APIKey, error) {
	return s.db.UpdateAPIKey(ctx, id, req)
}

// DeleteAPIKey removes an APIKey.
func (s *Service) DeleteAPIKey(ctx context.Context, id int64) error {
	return s.db.DeleteAPIKey(ctx, id)
}

// SubscribeUpdates registers an SSE connection's interest in
// conversationID's updates. The returned function must be called exactly
// once when the caller stops listening.
func (s *Service) SubscribeUpdates(conversationID int64) (<-chan struct{}, func()) {
	return s.updates.Subscribe(conversationID)
}

// HandleConversationEvent implements events.Handler.
func (s *Service) HandleConversationEvent(event eventmodels.ConversationEvent) {
	switch event.Event {
	case eventmodels.ConversationEventTerminate:
		s.terminations.Signal(event.ConversationID)
	case eventmodels.ConversationEventUpdated:
		s.updates.Signal(event.ConversationID)
	}
}

func (s *Service) publishUpdated(conversationID int64) {
	if err := s.publisher.Publish(eventmodels.ConversationEvent{
		ConversationID: conversationID,
		Event:          eventmodels.ConversationEventUpdated,
	}); err != nil {
		log.Error("failed to publish conversation update event: "+err.Error(), map[string]interface{}{"conversation-id": conversationID})
	}
}

// append re-acquires the processing lock and appends a single DialogEntry,
// atomically, then notifies other replicas so any SSE subscribers they are
// serving learn about it. It is the sole write path used while processing
// a conversation, per the README's "must start a new transaction for each
// message, re-acquire the lock, do the update, and commit" rule.
func (s *Service) append(ctx context.Context, conversationID int64, lockID string, entry persistence.NewDialogEntry) (restmodels.DialogEntry, error) {
	de, err := s.db.ReacquireLockAndAppend(ctx, conversationID, lockID, entry)
	if err != nil {
		return restmodels.DialogEntry{}, err
	}
	s.publishUpdated(conversationID)
	return de, nil
}

// appendBestEffort is used for entries recorded on a best-effort basis
// while already unwinding after an error (e.g. an AGENT_ERROR entry) --
// there is nothing further to do differently if this also fails, beyond
// logging it, since the caller is already on its way to releasing the
// lock and stopping.
func (s *Service) appendBestEffort(ctx context.Context, conversationID int64, lockID string, entry persistence.NewDialogEntry) {
	if _, err := s.append(ctx, conversationID, lockID, entry); err != nil && !errors.Is(err, persistence.ErrLockLost) {
		log.Error("failed to record dialog entry: "+err.Error(), map[string]interface{}{"conversation-id": conversationID})
	}
}
