-- M6: agent sidecar. Drafts the agent writes for a thread (never posted).
CREATE TABLE drafts (
  account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  thread_id   TEXT NOT NULL,
  repo        TEXT NOT NULL DEFAULT '',
  number      INTEGER NOT NULL DEFAULT 0,
  text        TEXT NOT NULL,
  model       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  PRIMARY KEY (account_id, thread_id)
);
