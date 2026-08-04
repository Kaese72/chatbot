package restmodels

import "time"

// APIKeyType is the LLM provider an APIKey authenticates against.
type APIKeyType string

const (
	APIKeyTypeAnthropic APIKeyType = "ANTHROPIC"
)

// APIKey is a configured LLM provider credential. The secret value itself
// is deliberately never returned by the API -- once submitted it can be
// replaced or deleted but not read back.
type APIKey struct {
	ID   int64      `json:"id"`
	Name string     `json:"name"`
	Type APIKeyType `json:"type"`
	// Active reports whether this is the key currently used for new LLM
	// requests. At most one APIKey is ever active at a time.
	Active  bool      `json:"active"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// NewAPIKeyRequest is the body of POST /api-keys.
type NewAPIKeyRequest struct {
	Name string     `json:"name" minLength:"1" doc:"A human-readable label for this key."`
	Type APIKeyType `json:"type" enum:"ANTHROPIC" doc:"The LLM provider this key authenticates against."`
	// Active, if true, makes this the active key and deactivates whichever
	// key was previously active, atomically.
	Active bool   `json:"active" doc:"Whether this key should immediately become the active key. At most one key is active at a time; setting this deactivates any other active key."`
	Value  string `json:"value" minLength:"1" doc:"The secret API key value."`
}

// UpdateAPIKeyRequest is the body of PATCH /api-keys/{id}. Every field is
// optional; omitted fields are left unchanged. Setting Active true
// deactivates whichever key was previously active, atomically.
type UpdateAPIKeyRequest struct {
	Name   *string `json:"name,omitempty" doc:"A human-readable label for this key."`
	Active *bool   `json:"active,omitempty" doc:"Whether this key should be the active key. Setting true deactivates any other active key; setting false simply deactivates this one."`
	Value  *string `json:"value,omitempty" doc:"Replace the secret API key value."`
}

// APIKeyList is the response body of GET /api-keys.
type APIKeyList struct {
	APIKeys []APIKey `json:"api-keys"`
}
