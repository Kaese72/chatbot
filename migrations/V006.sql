-- Delete conversations that predate per-user ownership (V005). They were
-- global, have no owner, and are invisible to every user since all
-- user-facing queries filter on owner_id. Dialog entries go first because of
-- fk_dialog_entries_conversation.
DELETE FROM dialog_entries
WHERE conversation_id IN (SELECT id FROM conversations WHERE owner_id IS NULL);

DELETE FROM conversations WHERE owner_id IS NULL;
