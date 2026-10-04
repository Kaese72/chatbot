package serviceauth

import "context"

type actingUserContextKey struct{}

// ContextWithActingUser returns a copy of ctx carrying userID as the acting
// user -- the human whose turn is currently being processed, and whose own
// identity any outbound tool call made during ctx's lifetime should be
// attributed to. See conversation.Service.New/Input, where it is set, and
// Provider.Token, where it is read.
func ContextWithActingUser(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, actingUserContextKey{}, userID)
}

// ActingUser returns the acting user ID set by ContextWithActingUser, and
// false if none was set.
func ActingUser(ctx context.Context) (int64, bool) {
	userID, ok := ctx.Value(actingUserContextKey{}).(int64)
	return userID, ok
}
