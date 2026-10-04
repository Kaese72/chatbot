-- The chatbot no longer self-provisions an authentication-service user to
-- act on other services as (see V003.sql). It now authenticates to
-- authentication's internal listener as its own Kubernetes ServiceAccount
-- and impersonates the conversation's owner per-request instead -- see
-- internal/serviceauth. The identity table and the credential it held are
-- no longer needed.
DROP TABLE identity;
