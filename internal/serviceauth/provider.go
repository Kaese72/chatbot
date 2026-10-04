// Package serviceauth authenticates this process to the authentication
// service's internal listener as its own Kubernetes ServiceAccount (see
// huemie-lib/k8sauth on the authentication side), and exchanges that for a
// short-lived use-token impersonating whichever human user triggered the
// conversation turn currently being processed. Replaces the chatbot's old
// self-provisioned authentication-service user (acting as the bot itself
// for every tool call, regardless of who was driving the conversation).
package serviceauth

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Kaese72/chatbot/internal/authclient"
)

// Provider produces tokens for outbound calls to other services, acting as
// whichever user ContextWithActingUser set on the call's context.
type Provider struct {
	tokenPath string
	auth      *authclient.Client
}

// NewProvider builds a Provider that reads this pod's own projected,
// audience-bound Kubernetes ServiceAccount token from tokenPath and presents
// it to auth's internal impersonation endpoint.
func NewProvider(tokenPath string, auth *authclient.Client) *Provider {
	return &Provider{tokenPath: tokenPath, auth: auth}
}

// Token reads the acting user set on ctx (see ContextWithActingUser) and
// exchanges this pod's own Kubernetes ServiceAccount token for a short-lived
// use-token impersonating that user. Named generically, rather than after
// any one outbound client, because it is just a
// func(context.Context) (string, error) -- usable as the tokenProvider for
// devicestore.NewClient today, and for any future outbound client the same
// way.
//
// Fails loudly (rather than silently proceeding with no acting user) if ctx
// carries none -- that should never happen once conversation.Service.New
// and Input are wired to set it, and a silent fallback would defeat the
// entire point of this mechanism.
func (p *Provider) Token(ctx context.Context) (string, error) {
	actingUserID, ok := ActingUser(ctx)
	if !ok {
		return "", fmt.Errorf("no acting user set on context; refusing to proceed")
	}
	// Re-read on every call, rather than caching: kubelet periodically
	// rotates a projected ServiceAccount token's contents in place.
	serviceToken, err := os.ReadFile(p.tokenPath)
	if err != nil {
		return "", fmt.Errorf("failed to read service account token: %w", err)
	}
	return p.auth.Impersonate(ctx, strings.TrimSpace(string(serviceToken)), actingUserID)
}
