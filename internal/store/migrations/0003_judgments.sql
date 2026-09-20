-- M2: typed judgments per thread version, plus enrichment fields on threads so
-- the judge sees the item body and the latest activity.

CREATE TABLE judgments (
  account_id         INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  thread_id          TEXT NOT NULL,
  thread_version     TEXT NOT NULL,        -- hash(updated_at, latest_comment_url, unread); stale when it differs
  questions_version  TEXT NOT NULL,        -- e.g. triage.v1
  provider           TEXT NOT NULL,        -- jev | ollama | fake
  model              TEXT NOT NULL DEFAULT '',
  calibrated         INTEGER NOT NULL DEFAULT 0,
  answers_json       TEXT NOT NULL,        -- map[question id]judge.Answer
  usage_json         TEXT NOT NULL DEFAULT '{}',
  latency_ms         INTEGER NOT NULL DEFAULT 0,
  created_at         TEXT NOT NULL,
  PRIMARY KEY (account_id, thread_id, questions_version)
);

ALTER TABLE threads ADD COLUMN item_author      TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN item_state       TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN item_draft       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE threads ADD COLUMN item_labels      TEXT NOT NULL DEFAULT '[]';
ALTER TABLE threads ADD COLUMN item_body        TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN latest_author    TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN latest_body      TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN latest_at        TEXT;
ALTER TABLE threads ADD COLUMN enriched_version TEXT NOT NULL DEFAULT '';
