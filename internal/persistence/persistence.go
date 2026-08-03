// Package persistence defines the storage contract for conversations and
// dialog entries, independent of the concrete database backend. See
// internal/persistence/mariadb for the MariaDB implementation.
package persistence

import (
	"context"
	"errors"

	"github.com/Kaese72/chatbot/restmodels"
)

// Sentinel errors returned by Persistence implementations. Callers
// (internal/conversation, internal/restwebapp) use errors.Is against these
// to decide which HTTP status / control-flow branch applies; they must not
// depend on implementation-specific error text.
var (
	// ErrConversationNotFound means no conversation exists with the given ID.
	ErrConversationNotFound = errors.New("conversation not found")
	// ErrConversationNotAwaitingInput means the conversation exists but its
	// Initiative is not USER, so new input cannot be accepted and it cannot
	// be forgotten right now.
	ErrConversationNotAwaitingInput = errors.New("conversation is not awaiting user input")
	// ErrConversationLocked means another replica currently holds an
	// unexpired processing lock on the conversation.
	ErrConversationLocked = errors.New("conversation is currently locked by another replica")
	// ErrLockLost means the caller's lock ID no longer owns the
	// conversation's processing lock (it was stolen after expiring, or
	// released). Per the README, processing must stop immediately without
	// writing anything further or returning any output to the user.
	ErrLockLost = errors.New("processing lock was lost")
)

// NewDialogEntry describes a DialogEntry to be appended to a conversation.
// Payload is marshaled directly into the entry's stored content and must be
// one of the restmodels payload types matching Type (or struct{}{} for
// types with no payload, e.g. USER_STOP).
type NewDialogEntry struct {
	Type       restmodels.DialogEntryType
	Initiative restmodels.Initiative
	Payload    any
}

// Persistence is the storage contract the rest of the service depends on.
type Persistence interface {
	// ListConversations returns every conversation, most recently updated
	// first.
	ListConversations(ctx context.Context) ([]restmodels.Conversation, error)

	// GetConversation returns a single conversation by ID, or
	// ErrConversationNotFound.
	GetConversation(ctx context.Context, conversationID int64) (restmodels.Conversation, error)

	// BeginConversation creates a brand new conversation already locked by
	// lockID with Initiative AGENT, and records the initial USER_INPUT
	// DialogEntry, atomically.
	BeginConversation(ctx context.Context, lockID string, query string) (restmodels.Conversation, error)

	// BeginInput atomically: (1) transitions an existing conversation's
	// Initiative from USER to AGENT, refusing if it isn't currently USER
	// (ErrConversationNotAwaitingInput) or doesn't exist
	// (ErrConversationNotFound); (2) acquires the processing lock for
	// lockID, refusing if another replica already holds an unexpired lock
	// (ErrConversationLocked); (3) records the USER_INPUT DialogEntry.
	// Mirrors the README's "Replica Conversation lock" section: the
	// initiative is changed first, then the lock is acquired within that
	// same transaction.
	BeginInput(ctx context.Context, conversationID int64, lockID string, query string) error

	// ReacquireLockAndAppend atomically re-acquires the processing lock for
	// lockID and appends a single DialogEntry, per the README's "must start
	// a new transaction for each message, re-acquire the lock, do the
	// update, and commit" rule. Returns ErrLockLost if lockID no longer
	// owns the lock (stolen after expiry) — the caller must treat this
	// exactly like a termination and stop processing immediately, without
	// writing anything further or responding to the user.
	ReacquireLockAndAppend(ctx context.Context, conversationID int64, lockID string, entry NewDialogEntry) (restmodels.DialogEntry, error)

	// ReleaseLock hands the initiative back to the user and clears the
	// processing lock, but only if lockID still owns it. It is
	// deliberately not an error for lockID to no longer own the lock (that
	// only means someone else already released or took over it), so
	// callers can call it unconditionally during cleanup.
	ReleaseLock(ctx context.Context, conversationID int64, lockID string) error

	// ListDialogEntries returns a conversation's DialogEntries in ascending
	// ID order. Pass afterID == 0 for the full history, or a specific ID to
	// fetch only entries after it (used by the follow/SSE endpoint and to
	// resume delivery after a reconnect).
	ListDialogEntries(ctx context.Context, conversationID int64, afterID int64) ([]restmodels.DialogEntry, error)

	// ForgetConversation deletes a conversation and all of its
	// DialogEntries. Only permitted while Initiative is USER. Returns
	// ErrConversationNotFound or ErrConversationNotAwaitingInput otherwise.
	ForgetConversation(ctx context.Context, conversationID int64) error
}
