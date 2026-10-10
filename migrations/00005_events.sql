-- +goose Up
-- M1.1a: events, occurrences, and registrations replace meetings and RSVPs.
-- Nothing is converted (DEC-22): no church is running Fold. Constraint and
-- index names match what GORM AutoMigrate expects, so the drift check stays
-- quiet. CHECK constraints are extra; GORM does not emit them.

DROP TABLE rsvps;
DROP TABLE meeting_exceptions;
DROP TABLE meetings;

CREATE TABLE events (
    id bigserial PRIMARY KEY,
    group_id bigint NOT NULL CONSTRAINT fk_events_group REFERENCES groups (id),
    title text NOT NULL,
    starts_local text NOT NULL,
    duration_minutes bigint NOT NULL,
    tzid text NOT NULL,
    rrule text NOT NULL,
    rrule_until text,
    location text NOT NULL,
    created_by bigint NOT NULL CONSTRAINT fk_events_creator REFERENCES users (id),
    created_at timestamptz,
    updated_at timestamptz,
    deleted_at timestamptz,
    CONSTRAINT events_duration_check CHECK (duration_minutes > 0)
);
CREATE INDEX idx_events_group_id ON events (group_id);
CREATE INDEX idx_events_deleted_at ON events (deleted_at);

CREATE TABLE event_occurrences (
    id bigserial PRIMARY KEY,
    event_id bigint NOT NULL CONSTRAINT fk_event_occurrences_event REFERENCES events (id),
    original_date text NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'scheduled',
    title_override text,
    location_override text,
    starts_at_override timestamptz,
    duration_override bigint,
    created_at timestamptz,
    updated_at timestamptz,
    CONSTRAINT event_occurrences_status_check CHECK (status IN ('scheduled', 'cancelled')),
    CONSTRAINT event_occurrences_duration_check CHECK (duration_override IS NULL OR duration_override > 0)
);
CREATE UNIQUE INDEX idx_event_original_date ON event_occurrences (event_id, original_date);
CREATE INDEX idx_event_occurrences_starts_at ON event_occurrences (starts_at);

CREATE TABLE registrations (
    id bigserial PRIMARY KEY,
    occurrence_id bigint NOT NULL CONSTRAINT fk_registrations_occurrence REFERENCES event_occurrences (id),
    user_id bigint NOT NULL CONSTRAINT fk_registrations_user REFERENCES users (id),
    status text NOT NULL,
    created_at timestamptz,
    updated_at timestamptz,
    CONSTRAINT registrations_status_check CHECK (status IN ('going', 'maybe', 'declined'))
);
CREATE UNIQUE INDEX idx_registration ON registrations (occurrence_id, user_id);

-- +goose Down
DROP TABLE registrations;
DROP TABLE event_occurrences;
DROP TABLE events;

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
