package events

import "sync"

// Registry is an in-process fan-out from conversation ID to a set of
// registered channels. It backs two independent uses within a single
// replica:
//
//   - Termination signalling: the goroutine currently processing a
//     conversation on this replica subscribes with the conversation's ID;
//     when a "terminate" ConversationEvent arrives from RabbitMQ (which
//     every replica receives), the handler calls Signal, and only the
//     replica that actually has a subscriber for that ID does anything.
//   - SSE update fan-out: every open .../follow/{id} connection on this
//     replica subscribes; when an "updated" ConversationEvent arrives
//     (published by whichever replica is actually processing the
//     conversation, which may be a different replica than the one serving
//     the SSE connection), every local subscriber is woken up to re-read
//     new DialogEntries from the database.
type Registry struct {
	mu   sync.Mutex
	subs map[int64]map[int]chan struct{}
	next int
}

func NewRegistry() *Registry {
	return &Registry{subs: make(map[int64]map[int]chan struct{})}
}

// Subscribe registers a new buffered channel for conversationID and
// returns it along with an unsubscribe function. The caller must invoke
// the unsubscribe function exactly once when it stops listening.
func (r *Registry) Subscribe(conversationID int64) (<-chan struct{}, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := make(chan struct{}, 1)
	id := r.next
	r.next++
	if r.subs[conversationID] == nil {
		r.subs[conversationID] = make(map[int]chan struct{})
	}
	r.subs[conversationID][id] = ch

	unsubscribe := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.subs[conversationID], id)
		if len(r.subs[conversationID]) == 0 {
			delete(r.subs, conversationID)
		}
	}
	return ch, unsubscribe
}

// Signal wakes every channel currently subscribed to conversationID. It
// never blocks: each channel is buffered to depth 1, and a signal that
// arrives while one is already pending is coalesced with it — subscribers
// are expected to re-read authoritative state (the database) on wakeup
// rather than treating the signal itself as carrying data.
func (r *Registry) Signal(conversationID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ch := range r.subs[conversationID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// HasSubscriber reports whether this replica currently has at least one
// subscriber registered for conversationID. Used by termination handling to
// decide whether this replica is the one actually processing the
// conversation.
func (r *Registry) HasSubscriber(conversationID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subs[conversationID]) > 0
}
