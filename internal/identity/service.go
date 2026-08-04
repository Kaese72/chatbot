// Package identity implements the chatbot's own identity in the
// authentication service: a real, ordinary user the bot creates for itself
// via POST /identities/setup and thereafter authenticates as before every
// tool call that acts on another service (currently device-store), instead
// of a static deploy-time credential. See the chatbot's README for the full
// design rationale and its documented limitations.
package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/Kaese72/chatbot/internal/authclient"
	"github.com/Kaese72/chatbot/internal/persistence"
)

type Service struct {
	db   persistence.Persistence
	auth *authclient.Client
}

func NewService(db persistence.Persistence, auth *authclient.Client) *Service {
	return &Service{db: db, auth: auth}
}

// Setup creates a brand-new user in the authentication service -- named
// name, with a freshly generated username/password the caller never sees --
// using callerToken's authority, then saves that username/password as the
// chatbot's own identity. Any identity saved previously is replaced
// wholesale: this is also the recovery path if the underlying
// authentication-service user was deleted out from under the chatbot, since
// there is no way for the chatbot to be notified of that happening.
func (s *Service) Setup(ctx context.Context, callerToken string, name string) error {
	username, err := randomToken("chatbot-", 8)
	if err != nil {
		return fmt.Errorf("failed to generate identity username: %w", err)
	}
	password, err := randomToken("", 32)
	if err != nil {
		return fmt.Errorf("failed to generate identity password: %w", err)
	}

	if _, err := s.auth.CreateUser(ctx, callerToken, username, password, name); err != nil {
		return fmt.Errorf("failed to create authentication-service user: %w", err)
	}
	if err := s.db.SaveIdentity(ctx, username, password); err != nil {
		return fmt.Errorf("failed to save chatbot identity: %w", err)
	}
	return nil
}

// Status reports whether an identity has been saved, for GET
// /identities/status.
func (s *Service) Status(ctx context.Context) (bool, error) {
	return s.db.IdentityConfigured(ctx)
}

// DeviceStoreToken logs in as the saved chatbot identity and returns a
// fresh use-token to present to device-store (or any other service
// verified by the same authentication service). It is passed directly to
// devicestore.NewClient as that client's token provider, so every tool call
// authenticates fresh rather than reusing a token that may have expired
// (use-tokens are short-lived, see the authentication service's README) or
// belong to a since-deleted user.
//
// Returns persistence.ErrIdentityNotConfigured if POST /identities/setup
// has never been run. If it has, but login still fails (most likely because
// the authentication-service user was deleted directly, bypassing the
// chatbot entirely), the wrapped error says so and points at re-running
// setup -- the chatbot has no way to distinguish "deleted" from any other
// login failure without querying the authentication service's user list,
// which would require yet another privileged credential.
func (s *Service) DeviceStoreToken(ctx context.Context) (string, error) {
	username, password, err := s.db.IdentityCredentials(ctx)
	if err != nil {
		return "", err
	}
	token, err := s.auth.Login(ctx, username, password)
	if err != nil {
		return "", fmt.Errorf(
			"failed to authenticate as chatbot identity %q: %w (if this user was deleted, POST /chatbot-service/v0/identities/setup again)",
			username, err,
		)
	}
	return token, nil
}

func randomToken(prefix string, bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}
