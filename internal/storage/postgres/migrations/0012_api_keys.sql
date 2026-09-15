-- API keys, and the profile each one belongs to.
--
-- Replaces a single shared passphrase. A shared secret cannot answer the
-- questions that matter once more than one person holds it: who is making
-- this request, whose access do I revoke when a laptop is lost, and who was
-- using the system when something changed. A key per person answers all three
-- and costs nothing extra to check.
--
-- Keys are never stored. Only a SHA-256 digest is kept, so a copy of this
-- table is not a set of working credentials. SHA-256 rather than bcrypt is
-- deliberate: a key is 256 bits of machine-generated randomness, so there is
-- no dictionary to attack and no reason to pay a slow hash on every request.
-- That reasoning does not carry over to passwords, which are low-entropy and
-- do need bcrypt.
CREATE TABLE IF NOT EXISTS api_keys (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    -- The leading, non-secret part of the key. Stored in clear so a request
    -- can be matched to one row without scanning every digest, and so a key
    -- can be identified in logs and in the interface without revealing it.
    prefix      TEXT        NOT NULL UNIQUE,
    key_hash    BYTEA       NOT NULL,

    -- The profile.
    name        TEXT        NOT NULL CHECK (name <> ''),
    role        TEXT        NOT NULL DEFAULT 'operator'
                            CHECK (role IN ('owner', 'operator', 'viewer')),
    note        TEXT        NOT NULL DEFAULT '',

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Updated on use, best effort. Its purpose is answering "is this key
    -- still in use" before revoking it, which does not need to be exact.
    last_used_at TIMESTAMPTZ,
    -- Revocation is a timestamp rather than a delete, so a withdrawn key
    -- cannot be silently reissued and the record of who had access survives.
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS api_keys_active_idx ON api_keys (prefix) WHERE revoked_at IS NULL;
