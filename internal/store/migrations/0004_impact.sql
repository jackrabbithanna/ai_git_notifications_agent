-- M3: impact profiles (user-editable YAML), per-PR impact analyses, and the
-- item creation time (to tell brand-new PRs from pushes).

CREATE TABLE profiles (
  id          TEXT PRIMARY KEY,
  yaml        TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1,
  updated_at  TEXT NOT NULL
);

-- Built-in profiles can be disabled without copying them.
CREATE TABLE profile_flags (
  id       TEXT PRIMARY KEY,
  enabled  INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE pr_analysis (
  account_id         INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  repo               TEXT NOT NULL,
  number             INTEGER NOT NULL,
  forge              TEXT NOT NULL DEFAULT 'github',
  kind               TEXT NOT NULL DEFAULT 'pr',        -- pr | mr
  head_sha           TEXT NOT NULL DEFAULT '',
  thread_version     TEXT NOT NULL DEFAULT '',          -- version of the notification thread when analysed ('' for scans)
  profile_id         TEXT NOT NULL,
  title              TEXT NOT NULL DEFAULT '',
  html_url           TEXT NOT NULL DEFAULT '',
  author             TEXT NOT NULL DEFAULT '',
  state              TEXT NOT NULL DEFAULT '',          -- open | merged | closed
  merged_at          TEXT,
  updated_at         TEXT,
  report_json        TEXT NOT NULL DEFAULT '{}',        -- profiles.Report
  questions_version  TEXT NOT NULL DEFAULT '',
  provider           TEXT NOT NULL DEFAULT '',
  model              TEXT NOT NULL DEFAULT '',
  calibrated         INTEGER NOT NULL DEFAULT 0,
  answers_json       TEXT NOT NULL DEFAULT '{}',
  usage_json         TEXT NOT NULL DEFAULT '{}',
  impact_level       INTEGER NOT NULL DEFAULT -1,       -- -1 unanalysed, 0 none, 1 possible, 2 likely, 3 certain
  impact_score       REAL NOT NULL DEFAULT 0,
  change_kind        TEXT NOT NULL DEFAULT '',
  note_json          TEXT NOT NULL DEFAULT '',          -- llm.ImpactNote ('' = none yet)
  note_model         TEXT NOT NULL DEFAULT '',
  error              TEXT NOT NULL DEFAULT '',
  analysed_at        TEXT,
  created_at         TEXT NOT NULL,
  PRIMARY KEY (account_id, repo, number)
);
CREATE INDEX pr_analysis_by_level ON pr_analysis (impact_level DESC, updated_at DESC);

ALTER TABLE threads ADD COLUMN item_created_at TEXT;
ALTER TABLE sync_state ADD COLUMN last_landed_scan_at TEXT;
