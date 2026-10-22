-- +goose Up
-- M1.9 slice 1 (5.3, 5.10, DEC-8, DEC-23, DEC-32): households, the
-- accounts in them, household people with no login, and guardian links.
-- An account belongs to at most one household (user_id is unique).
-- Adding a child or teen household person writes a pending guardian
-- link that only an admin can confirm, with a check note; adding an
-- adult needs no approval. A guardian link names exactly one of a teen
-- account (teen_user_id, for later slices) or a household person
-- (person_id). Registrations do not change in this slice.

CREATE TABLE households (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    address text NOT NULL DEFAULT '',
    created_by bigint NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_households_created_by ON households (created_by);

CREATE TABLE household_accounts (
    id bigserial PRIMARY KEY,
    household_id bigint NOT NULL REFERENCES households (id),
    user_id bigint NOT NULL REFERENCES users (id),
    relation text NOT NULL CHECK (relation IN ('guardian', 'adult', 'teen')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_household_accounts_household_id ON household_accounts (household_id);
CREATE UNIQUE INDEX idx_household_accounts_user_id ON household_accounts (user_id);

CREATE TABLE household_people (
    id bigserial PRIMARY KEY,
    household_id bigint NOT NULL REFERENCES households (id),
    first_name text NOT NULL,
    last_name text NOT NULL DEFAULT '',
    kind text NOT NULL CHECK (kind IN ('child', 'teen', 'adult')),
    birth_date date,
    care_notes text NOT NULL DEFAULT '',
    photo_consent boolean NOT NULL DEFAULT false,
    created_by bigint NOT NULL REFERENCES users (id),
    converted_user_id bigint REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);
CREATE INDEX idx_household_people_household_id ON household_people (household_id);
CREATE INDEX idx_household_people_deleted_at ON household_people (deleted_at);

CREATE TABLE guardian_links (
    id bigserial PRIMARY KEY,
    guardian_user_id bigint NOT NULL REFERENCES users (id),
    teen_user_id bigint REFERENCES users (id),
    person_id bigint REFERENCES household_people (id),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'confirmed', 'rejected', 'revoked')),
    confirmed_by bigint REFERENCES users (id) ON DELETE SET NULL,
    check_note text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    CONSTRAINT guardian_links_exactly_one_check CHECK (
        (teen_user_id IS NOT NULL AND person_id IS NULL)
        OR (teen_user_id IS NULL AND person_id IS NOT NULL)
    )
);
CREATE INDEX idx_guardian_links_guardian_user_id ON guardian_links (guardian_user_id);
CREATE INDEX idx_guardian_links_teen_user_id ON guardian_links (teen_user_id);
CREATE INDEX idx_guardian_links_person_id ON guardian_links (person_id);
CREATE INDEX idx_guardian_links_status ON guardian_links (status);

-- +goose Down
DROP TABLE guardian_links;
DROP TABLE household_people;
DROP TABLE household_accounts;
DROP TABLE households;
