-- Backfill restmodels.DialogEntryTypeAgentInitiativeReleased for every
-- conversation that is currently sitting with the user holding initiative
-- (i.e. its most recent turn already ended before this DialogEntry type
-- existed) so old conversations aren't missing the "it's your turn again"
-- marker the client now relies on instead of polling GET /conversations/{id}.
--
-- Conversations currently AGENT_IN_PROGRESS are deliberately left alone --
-- the agent still holds initiative, and the real marker will be appended
-- when that in-flight turn actually finishes, via the application code
-- introduced alongside this migration.
INSERT INTO dialog_entries (conversation_id, type, initiative, content)
SELECT id, 'AGENT_INITIATIVE_RELEASED', 'AGENT', '{}'
FROM conversations
WHERE initiative = 'USER';
