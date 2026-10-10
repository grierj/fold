-- +goose Up
-- Clean baseline (DEC-22): the schema of the models at the start of M0.1.
-- Never edit this file; add a new migration instead. Constraint and index
-- names match what GORM expects, so the AutoMigrate drift check stays quiet.

CREATE TABLE users (
    id bigserial PRIMARY KEY,
    username text NOT NULL CONSTRAINT uni_users_username UNIQUE,
    password text NOT NULL,
    name text,
    display_name text,
    email text,
    phone text,
    avatar_key text,
    avatar_focal_x numeric DEFAULT 50,
    avatar_focal_y numeric DEFAULT 50,
    role text DEFAULT 'member',
    session_version bigint NOT NULL DEFAULT 0,
    created_at timestamptz,
    updated_at timestamptz,
    deleted_at timestamptz
);
CREATE INDEX idx_users_deleted_at ON users (deleted_at);

CREATE TABLE groups (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    description text,
    tags text,
    image_url text,
    location text,
    cover_focal_x numeric DEFAULT 50,
    cover_focal_y numeric DEFAULT 50,
    leader_id bigint NOT NULL CONSTRAINT fk_groups_leader REFERENCES users (id),
    status text DEFAULT 'pending',
    visibility text NOT NULL DEFAULT 'public',
    invite_only boolean NOT NULL DEFAULT false,
    deletion_requested boolean NOT NULL DEFAULT false,
    created_at timestamptz,
    updated_at timestamptz,
    deleted_at timestamptz
);
CREATE INDEX idx_groups_deleted_at ON groups (deleted_at);

CREATE TABLE meetings (
    id bigserial PRIMARY KEY,
    group_id bigint NOT NULL CONSTRAINT fk_meetings_group REFERENCES groups (id),
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    location text,
    recurrence_rule text,
    created_at timestamptz,
    updated_at timestamptz,
    deleted_at timestamptz
);
CREATE INDEX idx_meetings_group_id ON meetings (group_id);
CREATE INDEX idx_meetings_deleted_at ON meetings (deleted_at);

CREATE TABLE meeting_exceptions (
    id bigserial PRIMARY KEY,
    meeting_id bigint NOT NULL CONSTRAINT fk_meeting_exceptions_meeting REFERENCES meetings (id),
    date text NOT NULL,
    cancelled boolean DEFAULT false
);
CREATE UNIQUE INDEX idx_meeting_date ON meeting_exceptions (meeting_id, date);

CREATE TABLE memberships (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL CONSTRAINT fk_memberships_user REFERENCES users (id),
    group_id bigint NOT NULL CONSTRAINT fk_memberships_group REFERENCES groups (id),
    role text DEFAULT 'member',
    status text DEFAULT 'active',
    joined_at timestamptz
);
CREATE UNIQUE INDEX idx_user_group ON memberships (user_id, group_id);

CREATE TABLE rsvps (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL CONSTRAINT fk_rsvps_user REFERENCES users (id),
    meeting_id bigint NOT NULL CONSTRAINT fk_rsvps_meeting REFERENCES meetings (id),
    date text NOT NULL,
    status text NOT NULL,
    created_at timestamptz,
    updated_at timestamptz
);
CREATE UNIQUE INDEX idx_rsvp ON rsvps (user_id, meeting_id, date);

CREATE TABLE settings (
    key text PRIMARY KEY,
    value text
);

-- +goose Down
DROP TABLE settings, rsvps, memberships, meeting_exceptions, meetings, groups, users;
