package restmodels

import "time"

// ConversationStatus is the top-level state of a Conversation, per the
// README's "Conversation" section.
type ConversationStatus string

const (
	ConversationStatusUserInput       ConversationStatus = "USER_INPUT"
	ConversationStatusAgentInProgress ConversationStatus = "AGENT_IN_PROGRESS"
)

// Conversation is the top-level entity used to list and navigate between
// conversations. The actual back-and-forth lives in DialogEntries.
type Conversation struct {
	ID int64 `json:"id"`
	// Status mirrors whether the conversation is waiting on the user or is
	// currently being processed by an agent replica.
	Status ConversationStatus `json:"status"`
	// Replica identifies which replica currently holds the processing lock,
	// if any ("Currently processed by what replica"). Nil when unlocked.
	Replica *string `json:"replica,omitempty"`
	// Initiative records who owns the next action; input can only be
	// accepted while Initiative is USER.
	Initiative Initiative `json:"initiative"`
	// Name is the first 255 characters of the first query, per the README.
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// NewConversationRequest is the body of POST /conversations/new.
type NewConversationRequest struct {
	Query string `json:"query" minLength:"1" doc:"The initial user query that starts the conversation."`
}

// InputRequest is the body of POST /conversations/{id}/input.
type InputRequest struct {
	Query string `json:"query" minLength:"1" doc:"The user's next input. The user must currently have the initiative."`
}

// ConversationList is the response body of GET /conversations.
type ConversationList struct {
	Conversations []Conversation `json:"conversations"`
}
