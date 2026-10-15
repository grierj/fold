-- +goose Up
-- M1.4 (C5, 5.3, 5.10, 10.1): guest registration for public events.
-- guest_signups holds the unverified intent (name, email, one occurrence)
-- until the emailed link is redeemed; no User or Registration exists
-- before that. registrations learn where they came from (source), the
-- hashed manage token a guest cancels with, and the cancelled status.

CREATE TABLE guest_signups (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    email citext NOT NULL,
    occurrence_id bigint NOT NULL CONSTRAINT fk_guest_signups_occurrence REFERENCES event_occurrences (id),
    create_account boolean NOT NULL DEFAULT false,
    token_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    confirmed_at timestamptz,
    ip text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_guest_signups_email ON guest_signups (email);
CREATE INDEX idx_guest_signups_occurrence_id ON guest_signups (occurrence_id);
CREATE UNIQUE INDEX idx_guest_signups_token_hash ON guest_signups (token_hash);

ALTER TABLE registrations ADD COLUMN source text NOT NULL DEFAULT 'web'
    CONSTRAINT registrations_source_check CHECK (source IN ('web', 'admin', 'guest_form'));
ALTER TABLE registrations ADD COLUMN manage_token_hash text NOT NULL DEFAULT '';

ALTER TABLE registrations DROP CONSTRAINT registrations_status_check;
ALTER TABLE registrations ADD CONSTRAINT registrations_status_check
    CHECK (status IN ('going', 'maybe', 'declined', 'cancelled'));

-- +goose Down
ALTER TABLE registrations DROP CONSTRAINT registrations_status_check;
ALTER TABLE registrations ADD CONSTRAINT registrations_status_check
    CHECK (status IN ('going', 'maybe', 'declined'));
ALTER TABLE registrations DROP COLUMN manage_token_hash;
ALTER TABLE registrations DROP COLUMN source;
DROP TABLE guest_signups;
