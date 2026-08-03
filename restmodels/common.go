package restmodels

// Initiative distinguishes whether the AGENT or the USER currently owns the
// next action in a Conversation, and, per DialogEntry, which side produced
// that entry.
type Initiative string

const (
	InitiativeAgent Initiative = "AGENT"
	InitiativeUser  Initiative = "USER"
)
