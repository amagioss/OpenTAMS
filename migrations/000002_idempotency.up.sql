CREATE TABLE idempotency_keys (
    key             VARCHAR(255) PRIMARY KEY,
    body_hash       TEXT NOT NULL,
    in_flight       BOOLEAN NOT NULL DEFAULT true,
    response_status INT NOT NULL DEFAULT 0,
    response_body   JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX idempotency_keys_expires ON idempotency_keys (expires_at);
