package restwebapp

import (
	"context"

	log "github.com/Kaese72/huemie-lib/logging"
	"github.com/danielgtaylor/huma/v2/sse"
)

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
	// Subscribe before the first read so that an update published between
	// "read the current state" and "start listening" is never missed.
	updateCh, unsubscribe := app.conversations.SubscribeUpdates(input.ConversationID)
	defer unsubscribe()

	lastSent := input.AfterDialogEntryID

	deliver := func() bool {
		entries, err := app.conversations.ListDialogEntries(ctx, input.ConversationID, lastSent)
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
