package restmodels

// SetupIdentityRequest is the body of POST /identities/setup.
type SetupIdentityRequest struct {
	Name string `json:"name" minLength:"1" doc:"Display name the chatbot's own user is created with in the authentication service."`
}

// IdentityStatus is the response body of GET /identities/status -- enough
// for a UI to decide whether to show onboarding, without exposing the saved
// credential.
type IdentityStatus struct {
	Configured bool `json:"configured" doc:"Whether a chatbot identity has been created and saved. Tool calls that act on other services fail until this is true."`
}
