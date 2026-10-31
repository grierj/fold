-- +goose Up
-- Background jobs (M3.1, PLAN 5.5): one table, worked by the same binary.
-- Workers claim due rows with FOR UPDATE SKIP LOCKED, so two app processes
-- never run one job twice. A failed attempt waits (status failed) until its
-- backoff ends; after max_attempts it is dead and waits for an admin on the
-- System page, who may retry it (queued again) or dismiss it. unique_key
-- makes scheduling idempotent, for example "accounts.age_rollover:2026-10-31".
CREATE TABLE jobs (
    id bigserial PRIMARY KEY,
    kind text NOT NULL CHECK (length(kind) BETWEEN 1 AND 100),
    payload jsonb NOT NULL DEFAULT '{}',
    run_at timestamptz NOT NULL DEFAULT now(),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed', 'dead', 'dismissed')),
    attempts bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts bigint NOT NULL DEFAULT 5 CHECK (max_attempts BETWEEN 1 AND 100),
    locked_at timestamptz,
    locked_by text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    unique_key text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_jobs_unique_key ON jobs (unique_key);
-- What a worker looks for: due work, and running work whose lease ran out.
CREATE INDEX idx_jobs_due ON jobs (run_at, id) WHERE status IN ('queued', 'failed');
CREATE INDEX idx_jobs_running ON jobs (locked_at) WHERE status = 'running';
-- The System page's list, and the cleanup of finished jobs.
CREATE INDEX idx_jobs_status ON jobs (status, updated_at);

-- +goose Down
DROP TABLE jobs;
