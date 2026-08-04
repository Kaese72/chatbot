package restmodels

// ServiceStatus is the response body of GET /status -- a single combined
// readiness check covering everything a UI needs before it can usefully
// start a conversation: whether an LLM API key is active, and whether the
// chatbot's own identity has been set up (see IdentityStatus for the
// identity-only equivalent).
type ServiceStatus struct {
	Identity bool `json:"identity" doc:"Whether a chatbot identity has been created and saved."`
	APIKey   bool `json:"api-key" doc:"Whether an active Anthropic API key is configured."`
}
