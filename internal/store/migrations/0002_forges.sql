-- M1.5: second forge (GitLab). Accounts learn their forge and token scopes,
-- threads learn who acted last, and GitLab accounts get a list of watched
-- projects whose activity is polled (GitLab has no notifications feed).

ALTER TABLE accounts ADD COLUMN forge TEXT NOT NULL DEFAULT 'github';        -- github | gitlab
ALTER TABLE accounts ADD COLUMN token_scopes TEXT NOT NULL DEFAULT '';       -- comma list; '' = unknown
ALTER TABLE threads ADD COLUMN actor TEXT NOT NULL DEFAULT '';               -- login of whoever caused the latest activity

CREATE TABLE watched_projects (
  account_id     INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  path           TEXT NOT NULL,                  -- e.g. dev/core
  project_id     INTEGER NOT NULL DEFAULT 0,     -- resolved lazily; 0 = unresolved
  last_event_at  TEXT,
  last_error     TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  PRIMARY KEY (account_id, path)
);
