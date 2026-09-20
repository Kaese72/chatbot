package restwebapp

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Kaese72/authentication/usertoken"
	log "github.com/Kaese72/huemie-lib/logging"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
)

// RequireConversationOwner is a huma operation middleware for the SSE follow
// route. An SSE handler can't return an error (by the time it runs, the
// response is already a 200 event stream), so ownership has to be verified
// before the stream starts; otherwise someone else's conversation would
// answer 200 with an empty stream instead of the same 404 every other route
// gives for a conversation that isn't theirs.
func (app *WebApp) RequireConversationOwner(api huma.API) func(ctx huma.Context, next func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		ownerID, ok := usertoken.UserID(ctx.Context())
		if !ok {
			_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		conversationID, err := strconv.ParseInt(ctx.Param("conversationID"), 10, 64)
		if err != nil {
			_ = huma.WriteErr(api, ctx, http.StatusNotFound, "conversation not found")
			return
		}
		if _, err := app.conversations.GetConversation(ctx.Context(), ownerID, conversationID); err != nil {
			mapped := mapServiceError(err)
			status := http.StatusInternalServerError
			var statusErr huma.StatusError
			if errors.As(mapped, &statusErr) {
				status = statusErr.GetStatus()
			}
			_ = huma.WriteErr(api, ctx, status, mapped.Error())
			return
		}
		next(ctx)
	}
}

// FollowConversation implements GET
// /chatbot-service/v0/conversations/{conversationID}/follow/{dialogEntryID}:
// an SSE stream of DialogEntries, starting with anything after
// AfterDialogEntryID that's already persisted, then continuing live.
//
// Cross-replica delivery works like this: whichever replica is actually
// processing the conversation publishes a "conversation updated"
// RabbitMQ event on every DialogEntry write. Every replica (including this
// one, which may not be the one processing the conversation) receives that
// event and signals its local update registry; this handler is woken up,
// re-reads DialogEntries after the last one it sent, and forwards anything
// new. The database, not the signal itself, is always the source of truth,
// so a missed or coalesced signal cannot cause a missed update -- only a
// delayed one, corrected on the next signal.
func (app *WebApp) FollowConversation(ctx context.Context, input *struct {
	ConversationID     int64 `path:"conversationID"`
	AfterDialogEntryID int64 `path:"dialogEntryID"`
}, send sse.Sender) {
	// Ownership was already verified by RequireConversationOwner, but the
	// caller's ID is still needed: ListDialogEntries re-checks it on every
	// delivery, which is a cheap primary-key lookup and also ends the stream
	// if the conversation is forgotten while it is open.
	ownerID, ok := usertoken.UserID(ctx)
	if !ok {
		return
	}

	// Subscribe before the first read so that an update published between
	// "read the current state" and "start listening" is never missed.
	updateCh, unsubscribe := app.conversations.SubscribeUpdates(input.ConversationID)
	defer unsubscribe()

	lastSent := input.AfterDialogEntryID

	deliver := func() bool {
		entries, err := app.conversations.ListDialogEntries(ctx, ownerID, input.ConversationID, lastSent)
		if err != nil {
			log.Error("failed to list dialog entries for SSE follow: "+err.Error(), map[string]interface{}{"conversation-id": input.ConversationID})
			return false
		}
		for _, entry := range entries {
			if err := send(sse.Message{ID: int(entry.ID), Data: entry}); err != nil {
				// The client disconnected or the connection otherwise
				// failed; stop trying to write to it.
				return false
			}
			lastSent = entry.ID
		}
		return true
	}

	if !deliver() {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updateCh:
			if !ok {
				return
			}
			if !deliver() {
				return
			}
		}
	}
}
