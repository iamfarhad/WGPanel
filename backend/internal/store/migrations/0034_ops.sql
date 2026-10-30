-- Ops package: node maintenance mode and SSH node provisioning jobs.

-- A node in maintenance keeps serving its existing peers but is skipped by steering,
-- automatic subscription/config selection and new-peer placement. The timestamp
-- doubles as the flag (NULL = in rotation) so the UI can show "since when" without a
-- second column. It lives on nodes, so it travels with panel backups.
ALTER TABLE nodes ADD COLUMN maintenance_since TIMESTAMPTZ;

-- SSH provisioning job history. Credentials are never stored - they live in API memory
-- for the life of the job only. log is the redacted, size-capped transcript. No FK to
-- nodes: the table is not backed up, and a TRUNCATE ... CASCADE during restore must
-- not be able to reach it (nor should deleting a node erase the audit trail).
CREATE TABLE ssh_provision_jobs (
    id            UUID PRIMARY KEY,
    node_id       UUID,
    host          TEXT NOT NULL,
    port          INTEGER NOT NULL,
    username      TEXT NOT NULL,
    fingerprint   TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    phase         TEXT NOT NULL DEFAULT '',
    error_code    TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    log           TEXT NOT NULL DEFAULT '',
    created_by    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ
);
CREATE INDEX idx_ssh_provision_jobs_created ON ssh_provision_jobs (created_at DESC);
