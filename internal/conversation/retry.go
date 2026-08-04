package conversation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Kaese72/chatbot/internal/persistence"
	log "github.com/Kaese72/huemie-lib/logging"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared"
)

// Retry policy for transient LLM failures (Anthropic overloaded, rate
// limited, a 5xx on their side, or a plain network blip). This is separate
// from the Anthropic SDK's own built-in retries: the SDK only retries
// before a streaming response has started (connection errors, or a
// non-200 on the initial request). An error like `overloaded_error` that
// arrives as an in-band SSE event *after* the stream already returned 200
// is not covered by that and reaches us as a stream error instead -- this
// is what these retries are for.
const (
	maxLLMRetries     = 5
	llmRetryBaseDelay = 2 * time.Second
	llmRetryMaxDelay  = 30 * time.Second
)

// isRetryableLLMError reports whether err is worth retrying with backoff,
// as opposed to a permanent failure (bad request, bad API key, a bug in
// how we called the API) that would just fail identically every time.
func isRetryableLLMError(err error) bool {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Type() {
		case shared.ErrorTypeOverloadedError, shared.ErrorTypeRateLimitError, shared.ErrorTypeAPIError, shared.ErrorTypeTimeoutError:
			return true
		default:
			// invalid_request_error, authentication_error, permission_error,
			// not_found_error, billing_error: retrying changes nothing.
			return false
		}
	}
	// No typed API error at all -- a connection reset, DNS failure, or
	// similar transport-level problem below the API layer. This is
	// exactly the class of error a retry is meant for.
	return true
}

// runTurnWithRetry wraps llm.Client.RunTurn with bounded exponential
// backoff for transient failures. Explicit stop signals
// (persistence.ErrLockLost, a termination request) are never retried --
// they propagate immediately, exactly as if retries didn't exist, since
// retrying past either would violate the README's "check before taking
// action" rule.
//
// A retry re-issues the same messages that were about to be sent; it does
// not attempt to patch up any content blocks the failed attempt already
// persisted mid-stream (persistBlock runs as blocks complete, before the
// turn as a whole is known to have failed). Any such block is a normal
// "call with no response yet" that the existing recovery in process()
// already handles the next time this conversation is picked up -- so a
// turn that exhausts its retries without ever completing self-heals on
// the next user input rather than needing special-cased cleanup here.
func (s *Service) runTurnWithRetry(ctx context.Context, conversationID int64, lockID string, apiKey string, terminationCh <-chan struct{}, messages []anthropic.MessageParam) (anthropic.Message, error) {
	delay := llmRetryBaseDelay

	for attempt := 0; ; attempt++ {
		message, err := s.llmClient.RunTurn(ctx, apiKey, messages, func(index int64, block anthropic.ContentBlockUnion) error {
			if perr := s.persistBlock(ctx, conversationID, lockID, block); perr != nil {
				return perr
			}
			if isTerminated(terminationCh) {
				return errTerminated
			}
			return nil
		})
		if err == nil {
			return message, nil
		}
		if errors.Is(err, persistence.ErrLockLost) || errors.Is(err, errTerminated) {
			return message, err
		}
		if attempt >= maxLLMRetries || !isRetryableLLMError(err) {
			return message, err
		}

		log.Info(fmt.Sprintf("retrying LLM request after transient error (attempt %d/%d): %s", attempt+1, maxLLMRetries, err.Error()), map[string]interface{}{"conversation-id": conversationID})

		select {
		case <-ctx.Done():
			return message, err
		case <-terminationCh:
			// A termination request arrived while we were waiting to
			// retry. Don't silently swallow it -- hand it back so the
			// caller treats this exactly like any other mid-turn
			// termination.
			return message, errTerminated
		case <-time.After(delay):
		}

		delay *= 2
		if delay > llmRetryMaxDelay {
			delay = llmRetryMaxDelay
		}
	}
}
