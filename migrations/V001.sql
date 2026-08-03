CREATE TABLE IF NOT EXISTS conversations (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    status     ENUM('USER_INPUT', 'AGENT_IN_PROGRESS') NOT NULL DEFAULT 'USER_INPUT',
    initiative ENUM('AGENT', 'USER')                    NOT NULL DEFAULT 'USER',
    name       VARCHAR(255)                             NOT NULL,
    -- lock_id identifies the replica (goroutine) currently processing this
    -- conversation, if any. locked_at is used to detect and take over
    -- abandoned locks after the configured timeout (default 300s).
    lock_id    VARCHAR(64)  NULL,
    locked_at  DATETIME(6)  NULL,
    created    DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated    DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),

    INDEX idx_conversations_updated (updated)
);

CREATE TABLE IF NOT EXISTS dialog_entries (
    -- A single AUTO_INCREMENT sequence shared by every conversation gives
    -- the global, always-increasing ordering the README requires. Gaps
    -- from a single conversation's perspective are expected and harmless.
    id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    conversation_id BIGINT UNSIGNED NOT NULL,
    created         DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    -- See restmodels.DialogEntryType for the full set of values.
    type            VARCHAR(64)     NOT NULL,
    initiative      ENUM('AGENT', 'USER') NOT NULL,
    -- Type-specific payload. Structured for every type except
    -- AGENT_GENERIC_TOOL_CALL / AGENT_GENERIC_TOOL_RESPONSE, which carry
    -- free-text tool name/input/output blobs, per the README.
    content         JSON            NOT NULL,

    CONSTRAINT fk_dialog_entries_conversation
        FOREIGN KEY (conversation_id) REFERENCES conversations (id),
    INDEX idx_dialog_entries_conversation_id (conversation_id, id)
);
