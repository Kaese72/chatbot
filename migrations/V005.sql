-- Conversations are private to the user that started them. owner_id is the
-- authentication-service user ID taken from the caller's use-token.
--
-- Nullable on purpose: conversations that existed before this migration were
-- global and have no meaningful owner. Every user-facing query filters on
-- owner_id = <caller>, and NULL never equals anything, so those legacy
-- conversations simply stop being visible to anyone rather than being handed
-- to an arbitrary user. New conversations always have an owner.
ALTER TABLE conversations
    ADD COLUMN owner_id BIGINT UNSIGNED NULL AFTER id,
    ADD INDEX idx_conversations_owner_updated (owner_id, updated);
