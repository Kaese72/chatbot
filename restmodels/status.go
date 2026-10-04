package restmodels

// ServiceStatus is the response body of GET /status -- a single combined
// readiness check covering everything a UI needs before it can usefully
// start a conversation: whether an LLM API key is active. There is no
// longer a chatbot identity to set up -- see internal/serviceauth.
type ServiceStatus struct {
	APIKey bool `json:"api-key" doc:"Whether an active Anthropic API key is configured."`
}
