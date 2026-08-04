-- The chatbot's own identity in the authentication service, used to call
-- other services (device-store) under a real, auditable user rather than a
-- static deploy-time credential. See internal/identity for the mechanism.
--
-- id is pinned to 1: at most one identity is ever saved. Re-running
-- POST /chatbot-service/v0/identities/setup (e.g. after the underlying
-- authentication-service user was deleted) replaces this row wholesale
-- rather than adding a second one.
CREATE TABLE IF NOT EXISTS identity (
    id       TINYINT UNSIGNED PRIMARY KEY DEFAULT 1,
    username VARCHAR(255) NOT NULL,
    password VARCHAR(255) NOT NULL,
    created  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),

    CONSTRAINT single_row CHECK (id = 1)
);
