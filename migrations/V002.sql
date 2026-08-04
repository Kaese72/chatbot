CREATE TABLE IF NOT EXISTS api_keys (
    id      BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name    VARCHAR(255)      NOT NULL,
    -- See restmodels.APIKeyType for the full set of values.
    type    ENUM('ANTHROPIC') NOT NULL,
    active  BOOLEAN           NOT NULL DEFAULT FALSE,
    value   VARCHAR(512)      NOT NULL,
    created DATETIME(6)       NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated DATETIME(6)       NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),

    -- Enforces "only one active key at a time" at the database level, not
    -- just in application code: active_slot collapses to NULL for every
    -- inactive row (NULLs never collide under a UNIQUE index) and to the
    -- literal value 1 for every active row, so a second concurrently
    -- activated row is rejected by the unique constraint instead of
    -- depending entirely on the application's read-then-write race window.
    active_slot TINYINT UNSIGNED AS (IF(active, 1, NULL)) VIRTUAL,
    UNIQUE KEY uq_api_keys_active_slot (active_slot)
);
