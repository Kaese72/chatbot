package eventmodels

// ConversationEventType distinguishes what kind of change happened to a
// Conversation.
type ConversationEventType string

const (
	// ConversationEventUpdated means one or more new DialogEntries were
	// persisted for the conversation. Replicas serving a
	// .../follow/{id} SSE stream for this conversation should re-read from
	// the database and forward anything new to their subscribers.
	ConversationEventUpdated ConversationEventType = "updated"
	// ConversationEventTerminate means a user has requested termination of
	// the conversation. The replica currently processing it (if any, and
	// if it is this replica) must stop taking further action.
	ConversationEventTerminate ConversationEventType = "terminate"
)

// ConversationEvent is published to the conversationEvents fanout exchange
// on every DialogEntry write and on every termination request, so that
// every replica can react regardless of which replica actually owns the
// conversation's processing lock or is serving a given SSE subscriber.
type ConversationEvent struct {
	ConversationID int64                 `json:"conversation-id"`
	Event          ConversationEventType `json:"event"`
}
