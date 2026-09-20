// Package store is the app's SQLite persistence layer (pure-Go driver).
package store

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB wraps the SQLite connection.
type DB struct {
	*sql.DB
}

// DefaultPath is $XDG_DATA_HOME/gitinbox/gitinbox.db (or ~/.local/share/...).
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "gitinbox", "gitinbox.db"), nil
}

// legacyAppName is the pre-rename data directory / file prefix.
const legacyAppName = "ghinbox"

// MigrateLegacyDataDir moves a pre-rename data directory (…/ghinbox with
// ghinbox.db) to the new location the first time the renamed app runs.
// Returns true when a migration happened.
func MigrateLegacyDataDir(newPath string) (bool, error) {
	newDir := filepath.Dir(newPath)
	if _, err := os.Stat(newDir); err == nil {
		return false, nil // already migrated or fresh install started
	}
	oldDir := filepath.Join(filepath.Dir(newDir), legacyAppName)
	if st, err := os.Stat(oldDir); err != nil || !st.IsDir() {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
		return false, err
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return false, fmt.Errorf("move %s → %s: %w", oldDir, newDir, err)
	}
	// Rename the database (and its WAL/SHM sidecars) and the log to the new prefix.
	newBase := strings.TrimSuffix(filepath.Base(newPath), ".db")
	for _, suffix := range []string{".db", ".db-wal", ".db-shm", ".log"} {
		from := filepath.Join(newDir, legacyAppName+suffix)
		to := filepath.Join(newDir, newBase+suffix)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, to); err != nil {
				return true, err
			}
		}
	}
	return true, nil
}

// Open opens (creating if needed) the database and applies pending migrations.
func Open(path string) (*DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	if path == ":memory:" {
		dsn = "file::memory:?cache=shared&_pragma=foreign_keys(1)"
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		sqlDB.SetMaxOpenConns(1)
	}
	db := &DB{sqlDB}
	if err := db.migrate(context.Background()); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("store: bad migration name %q", name)
		}
		if v <= current {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, fmtTime(time.Now())); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// --- time helpers -----------------------------------------------------------

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func fmtTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return fmtTime(*t)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func toJSON(v any) string {
	if v == nil {
		return "[]"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func fromJSONStrings(s string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

// --- accounts ---------------------------------------------------------------

// Account is a configured GitHub account (token lives in secrets, keyed by ID).
type Account struct {
	ID          int64     `json:"id"`
	Forge       string    `json:"forge"` // github | gitlab
	Login       string    `json:"login"`
	Host        string    `json:"host"`
	WriteMode   string    `json:"writeMode"`
	TokenScopes string    `json:"tokenScopes"` // comma list learned at add time; "" = unknown
	CreatedAt   time.Time `json:"createdAt"`
}

// Forge identifiers.
const (
	ForgeGitHub = "github"
	ForgeGitLab = "gitlab"
)

const accountColumns = `id, forge, login, host, write_mode, token_scopes, created_at`

func scanAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var created string
	if err := sc.Scan(&a.ID, &a.Forge, &a.Login, &a.Host, &a.WriteMode, &a.TokenScopes, &created); err != nil {
		return Account{}, err
	}
	a.CreatedAt = parseTime(created)
	return a, nil
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

func (db *DB) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (db *DB) GetAccount(ctx context.Context, id int64) (Account, error) {
	a, err := scanAccount(db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return a, err
}

// FindAccount looks an account up by login and host ("" meaning github.com).
func (db *DB) FindAccount(ctx context.Context, login, host string) (Account, error) {
	if host == "" {
		host = "github.com"
	}
	a, err := scanAccount(db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE login = ? AND host = ?`, login, host))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return a, err
}

// InsertAccount registers an account in read-only write mode.
func (db *DB) InsertAccount(ctx context.Context, forge, login, host string) (Account, error) {
	if forge == "" {
		forge = ForgeGitHub
	}
	if host == "" {
		if forge == ForgeGitLab {
			host = "gitlab.com"
		} else {
			host = "github.com"
		}
	}
	now := time.Now()
	res, err := db.ExecContext(ctx, `INSERT INTO accounts (forge, login, host, write_mode, token_scopes, created_at) VALUES (?, ?, ?, 'readonly', '', ?)`, forge, login, host, fmtTime(now))
	if err != nil {
		return Account{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Account{}, err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sync_state (account_id) VALUES (?)`, id); err != nil {
		return Account{}, err
	}
	return Account{ID: id, Forge: forge, Login: login, Host: host, WriteMode: "readonly", CreatedAt: now.UTC()}, nil
}

// SetAccountTokenScopes records the scopes learned for the account's token ("" = unknown).
func (db *DB) SetAccountTokenScopes(ctx context.Context, id int64, scopes string) error {
	_, err := db.ExecContext(ctx, `UPDATE accounts SET token_scopes = ? WHERE id = ?`, scopes, id)
	return err
}

func (db *DB) DeleteAccount(ctx context.Context, id int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	return err
}

func (db *DB) SetAccountWriteMode(ctx context.Context, id int64, mode string) error {
	res, err := db.ExecContext(ctx, `UPDATE accounts SET write_mode = ? WHERE id = ?`, mode, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- threads ----------------------------------------------------------------

// Thread is one notification thread plus local triage state.
type Thread struct {
	AccountID        int64      `json:"accountId"`
	ThreadID         string     `json:"threadId"`
	Repo             string     `json:"repo"`
	SubjectType      string     `json:"subjectType"`
	SubjectURL       string     `json:"subjectUrl"`
	SubjectNumber    int        `json:"subjectNumber"`
	HTMLURL          string     `json:"htmlUrl"`
	Title            string     `json:"title"`
	Reason           string     `json:"reason"`
	Actor            string     `json:"actor"` // login behind the latest activity, when known
	Unread           bool       `json:"unread"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	LastReadAt       *time.Time `json:"lastReadAt"`
	LatestCommentURL string     `json:"latestCommentUrl"`
	ActivityKind     string     `json:"activityKind"`
	RelationTags     []string   `json:"relationTags"`
	FilterVerdict    string     `json:"filterVerdict"`
	FilterReason     string     `json:"filterReason"`
	LocalReadAt      *time.Time `json:"localReadAt"`
	DoneAt           *time.Time `json:"doneAt"`
	SnoozedUntil     *time.Time `json:"snoozedUntil"`
	FirstSeenAt      time.Time  `json:"firstSeenAt"`
	LastSyncedAt     time.Time  `json:"lastSyncedAt"`
	Enrichment
}

// Enrichment is what the enrich step learned about the item and its latest activity.
type Enrichment struct {
	ItemAuthor      string     `json:"itemAuthor"`
	ItemState       string     `json:"itemState"`
	ItemDraft       bool       `json:"itemDraft"`
	ItemLabels      []string   `json:"itemLabels"`
	ItemBody        string     `json:"itemBody"`
	LatestAuthor    string     `json:"latestAuthor"`
	LatestBody      string     `json:"latestBody"`
	LatestAt        *time.Time `json:"latestAt"`
	ItemCreatedAt   *time.Time `json:"itemCreatedAt"`
	EnrichedVersion string     `json:"enrichedVersion"` // Thread.Version() at enrichment time
}

// IsBrandNew reports whether the notification's activity is the item's creation
// rather than a later push/update (needs enrichment; false when unknown).
func (t Thread) IsBrandNew() bool {
	if t.ItemCreatedAt == nil || t.EnrichedVersion == "" {
		return false
	}
	return t.UpdatedAt.Sub(*t.ItemCreatedAt) < 30*time.Minute
}

// Version fingerprints the thread's GitHub/GitLab-visible state; judgments and
// enrichment are keyed by it so new activity invalidates them (PLAN.md §3).
func (t Thread) Version() string {
	h := sha1.Sum([]byte(fmtTime(t.UpdatedAt) + "|" + t.LatestCommentURL + "|" + strconv.Itoa(boolInt(t.Unread))))
	return hex.EncodeToString(h[:8])
}

// IsRead is true when GitHub or the user marked the thread read.
func (t Thread) IsRead() bool { return !t.Unread || t.LocalReadAt != nil }

const threadColumns = `account_id, thread_id, repo, subject_type, subject_url, subject_number, html_url, title, reason, actor,
	unread, updated_at, last_read_at, latest_comment_url, activity_kind, relation_tags, filter_verdict, filter_reason,
	local_read_at, done_at, snoozed_until, first_seen_at, last_synced_at,
	item_author, item_state, item_draft, item_labels, item_body, latest_author, latest_body, latest_at, enriched_version, item_created_at`

func scanThread(sc interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var unread, draft int
	var updated, tags, firstSeen, lastSynced, labels string
	var lastRead, localRead, done, snoozed, latestAt, itemCreated sql.NullString
	err := sc.Scan(&t.AccountID, &t.ThreadID, &t.Repo, &t.SubjectType, &t.SubjectURL, &t.SubjectNumber, &t.HTMLURL, &t.Title, &t.Reason, &t.Actor,
		&unread, &updated, &lastRead, &t.LatestCommentURL, &t.ActivityKind, &tags, &t.FilterVerdict, &t.FilterReason,
		&localRead, &done, &snoozed, &firstSeen, &lastSynced,
		&t.ItemAuthor, &t.ItemState, &draft, &labels, &t.ItemBody, &t.LatestAuthor, &t.LatestBody, &latestAt, &t.EnrichedVersion, &itemCreated)
	if err != nil {
		return Thread{}, err
	}
	t.ItemCreatedAt = parseTimePtr(itemCreated)
	t.ItemDraft = draft != 0
	t.ItemLabels = fromJSONStrings(labels)
	t.LatestAt = parseTimePtr(latestAt)
	t.Unread = unread != 0
	t.UpdatedAt = parseTime(updated)
	t.LastReadAt = parseTimePtr(lastRead)
	t.RelationTags = fromJSONStrings(tags)
	t.LocalReadAt = parseTimePtr(localRead)
	t.DoneAt = parseTimePtr(done)
	t.SnoozedUntil = parseTimePtr(snoozed)
	t.FirstSeenAt = parseTime(firstSeen)
	t.LastSyncedAt = parseTime(lastSynced)
	return t, nil
}

// UpsertThread inserts or refreshes GitHub-sourced fields. Local triage state is
// preserved, except that new activity (updated_at advancing) clears done/snooze/
// local-read so the thread re-surfaces (PLAN.md §4.9).
func (db *DB) UpsertThread(ctx context.Context, t Thread) error {
	now := time.Now()
	if t.FirstSeenAt.IsZero() {
		t.FirstSeenAt = now
	}
	if t.LastSyncedAt.IsZero() {
		t.LastSyncedAt = now
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO threads (`+threadColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?, '', '', 0, '[]', '', '', '', NULL, '', NULL)
ON CONFLICT (account_id, thread_id) DO UPDATE SET
  repo = excluded.repo, subject_type = excluded.subject_type, subject_url = excluded.subject_url,
  subject_number = excluded.subject_number, html_url = excluded.html_url, title = excluded.title,
  reason = excluded.reason, actor = excluded.actor, unread = excluded.unread, updated_at = excluded.updated_at,
  last_read_at = excluded.last_read_at, latest_comment_url = excluded.latest_comment_url,
  activity_kind = excluded.activity_kind, relation_tags = excluded.relation_tags,
  filter_verdict = excluded.filter_verdict, filter_reason = excluded.filter_reason,
  local_read_at = CASE WHEN excluded.updated_at > threads.updated_at THEN NULL ELSE threads.local_read_at END,
  done_at       = CASE WHEN excluded.updated_at > threads.updated_at THEN NULL ELSE threads.done_at END,
  snoozed_until = CASE WHEN excluded.updated_at > threads.updated_at THEN NULL ELSE threads.snoozed_until END,
  last_synced_at = excluded.last_synced_at`,
		t.AccountID, t.ThreadID, t.Repo, t.SubjectType, t.SubjectURL, t.SubjectNumber, t.HTMLURL, t.Title, t.Reason, t.Actor,
		boolInt(t.Unread), fmtTime(t.UpdatedAt), fmtTimePtr(t.LastReadAt), t.LatestCommentURL, t.ActivityKind, toJSON(t.RelationTags),
		t.FilterVerdict, t.FilterReason, fmtTime(t.FirstSeenAt), fmtTime(t.LastSyncedAt))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ThreadQuery filters ListThreads. Zero values mean "no filter".
type ThreadQuery struct {
	AccountID    int64
	IncludeDone  bool
	IncludeNoise bool
	IncludeRead  bool // include threads read on GitHub or locally
	Snoozed      bool // include currently snoozed threads
	Repo         string
	Limit        int
}

// ListThreads returns threads newest-first according to the query.
func (db *DB) ListThreads(ctx context.Context, q ThreadQuery) ([]Thread, error) {
	var where []string
	var args []any
	if q.AccountID != 0 {
		where = append(where, "account_id = ?")
		args = append(args, q.AccountID)
	}
	if !q.IncludeDone {
		where = append(where, "done_at IS NULL")
	}
	if !q.IncludeNoise {
		where = append(where, "filter_verdict = 'keep'")
	}
	if !q.IncludeRead {
		where = append(where, "unread = 1 AND local_read_at IS NULL")
	}
	if !q.Snoozed {
		where = append(where, "(snoozed_until IS NULL OR snoozed_until <= ?)")
		args = append(args, fmtTime(time.Now()))
	}
	if q.Repo != "" {
		where = append(where, "repo = ?")
		args = append(args, q.Repo)
	}
	sqlStr := `SELECT ` + threadColumns + ` FROM threads`
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlStr += " ORDER BY updated_at DESC"
	if q.Limit > 0 {
		sqlStr += " LIMIT " + strconv.Itoa(q.Limit)
	}
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (db *DB) GetThread(ctx context.Context, accountID int64, threadID string) (Thread, error) {
	row := db.QueryRowContext(ctx, `SELECT `+threadColumns+` FROM threads WHERE account_id = ? AND thread_id = ?`, accountID, threadID)
	t, err := scanThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, ErrNotFound
	}
	return t, err
}

// Local triage state changes. Each returns ErrNotFound for unknown threads.

func (db *DB) MarkThreadRead(ctx context.Context, accountID int64, threadID string) error {
	return db.setLocal(ctx, accountID, threadID, `local_read_at = ?`, fmtTime(time.Now()))
}

func (db *DB) MarkThreadDone(ctx context.Context, accountID int64, threadID string) error {
	now := fmtTime(time.Now())
	return db.setLocal(ctx, accountID, threadID, `done_at = ?, local_read_at = COALESCE(local_read_at, ?)`, now, now)
}

func (db *DB) UndoThreadDone(ctx context.Context, accountID int64, threadID string) error {
	return db.setLocal(ctx, accountID, threadID, `done_at = NULL`)
}

func (db *DB) SnoozeThread(ctx context.Context, accountID int64, threadID string, until time.Time) error {
	return db.setLocal(ctx, accountID, threadID, `snoozed_until = ?`, fmtTime(until))
}

func (db *DB) UnsnoozeThread(ctx context.Context, accountID int64, threadID string) error {
	return db.setLocal(ctx, accountID, threadID, `snoozed_until = NULL`)
}

func (db *DB) setLocal(ctx context.Context, accountID int64, threadID, set string, args ...any) error {
	args = append(args, accountID, threadID)
	res, err := db.ExecContext(ctx, `UPDATE threads SET `+set+` WHERE account_id = ? AND thread_id = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkThreadsReadExcept flips unread=0 for the account's threads whose id starts
// with prefix and is not in keep — used to reconcile GitLab to-dos completed
// elsewhere (the pending list is authoritative).
func (db *DB) MarkThreadsReadExcept(ctx context.Context, accountID int64, prefix string, keep []string) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS keep_ids (id TEXT PRIMARY KEY)`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM keep_ids`); err != nil {
		return 0, err
	}
	for _, id := range keep {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO keep_ids (id) VALUES (?)`, id); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE threads SET unread = 0 WHERE account_id = ? AND unread = 1 AND thread_id LIKE ? || '%' AND thread_id NOT IN (SELECT id FROM keep_ids)`, accountID, prefix)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// SetThreadEnrichment stores what the enrich step learned, tagged with the version it saw.
func (db *DB) SetThreadEnrichment(ctx context.Context, accountID int64, threadID string, e Enrichment) error {
	res, err := db.ExecContext(ctx, `UPDATE threads SET item_author = ?, item_state = ?, item_draft = ?, item_labels = ?, item_body = ?,
		latest_author = ?, latest_body = ?, latest_at = ?, enriched_version = ?, item_created_at = ? WHERE account_id = ? AND thread_id = ?`,
		e.ItemAuthor, e.ItemState, boolInt(e.ItemDraft), toJSON(e.ItemLabels), e.ItemBody, e.LatestAuthor, e.LatestBody, fmtTimePtr(e.LatestAt), e.EnrichedVersion, fmtTimePtr(e.ItemCreatedAt),
		accountID, threadID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- judgments --------------------------------------------------------------

// Judgment is one stored judge response for a thread version.
type Judgment struct {
	AccountID        int64           `json:"accountId"`
	ThreadID         string          `json:"threadId"`
	ThreadVersion    string          `json:"threadVersion"`
	QuestionsVersion string          `json:"questionsVersion"`
	Provider         string          `json:"provider"`
	Model            string          `json:"model"`
	Calibrated       bool            `json:"calibrated"`
	AnswersJSON      json.RawMessage `json:"answers"`
	UsageJSON        json.RawMessage `json:"usage"`
	LatencyMs        int64           `json:"latencyMs"`
	CreatedAt        time.Time       `json:"createdAt"`
}

func (db *DB) PutJudgment(ctx context.Context, j Judgment) error {
	if len(j.UsageJSON) == 0 {
		j.UsageJSON = json.RawMessage("{}")
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO judgments (account_id, thread_id, thread_version, questions_version, provider, model, calibrated, answers_json, usage_json, latency_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id, thread_id, questions_version) DO UPDATE SET thread_version = excluded.thread_version, provider = excluded.provider,
  model = excluded.model, calibrated = excluded.calibrated, answers_json = excluded.answers_json, usage_json = excluded.usage_json,
  latency_ms = excluded.latency_ms, created_at = excluded.created_at`,
		j.AccountID, j.ThreadID, j.ThreadVersion, j.QuestionsVersion, j.Provider, j.Model, boolInt(j.Calibrated), string(j.AnswersJSON), string(j.UsageJSON), j.LatencyMs, fmtTime(time.Now()))
	return err
}

const judgmentColumns = `account_id, thread_id, thread_version, questions_version, provider, model, calibrated, answers_json, usage_json, latency_ms, created_at`

func scanJudgment(sc interface{ Scan(...any) error }) (Judgment, error) {
	var j Judgment
	var cal int
	var answers, usage, created string
	if err := sc.Scan(&j.AccountID, &j.ThreadID, &j.ThreadVersion, &j.QuestionsVersion, &j.Provider, &j.Model, &cal, &answers, &usage, &j.LatencyMs, &created); err != nil {
		return Judgment{}, err
	}
	j.Calibrated = cal != 0
	j.AnswersJSON = json.RawMessage(answers)
	j.UsageJSON = json.RawMessage(usage)
	j.CreatedAt = parseTime(created)
	return j, nil
}

func (db *DB) GetJudgment(ctx context.Context, accountID int64, threadID, questionsVersion string) (Judgment, error) {
	j, err := scanJudgment(db.QueryRowContext(ctx, `SELECT `+judgmentColumns+` FROM judgments WHERE account_id = ? AND thread_id = ? AND questions_version = ?`, accountID, threadID, questionsVersion))
	if errors.Is(err, sql.ErrNoRows) {
		return Judgment{}, ErrNotFound
	}
	return j, err
}

// ListJudgments returns judgments for a question set keyed by "<account>:<thread>" (accountID 0 = all).
func (db *DB) ListJudgments(ctx context.Context, accountID int64, questionsVersion string) (map[string]Judgment, error) {
	q := `SELECT ` + judgmentColumns + ` FROM judgments WHERE questions_version = ?`
	args := []any{questionsVersion}
	if accountID != 0 {
		q += " AND account_id = ?"
		args = append(args, accountID)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Judgment{}
	for rows.Next() {
		j, err := scanJudgment(rows)
		if err != nil {
			return nil, err
		}
		out[JudgmentKey(j.AccountID, j.ThreadID)] = j
	}
	return out, rows.Err()
}

// JudgmentKey is the map key used by ListJudgments.
func JudgmentKey(accountID int64, threadID string) string {
	return strconv.FormatInt(accountID, 10) + ":" + threadID
}

// JudgeCandidates returns visible, unread, kept threads whose judgment for the
// question set is missing or stale, newest first, up to limit.
func (db *DB) JudgeCandidates(ctx context.Context, accountID int64, questionsVersion string, limit int) ([]Thread, error) {
	threads, err := db.ListThreads(ctx, ThreadQuery{AccountID: accountID, Limit: limit * 3})
	if err != nil {
		return nil, err
	}
	have, err := db.ListJudgments(ctx, accountID, questionsVersion)
	if err != nil {
		return nil, err
	}
	out := make([]Thread, 0, limit)
	for _, t := range threads {
		if j, ok := have[JudgmentKey(t.AccountID, t.ThreadID)]; ok && j.ThreadVersion == t.Version() {
			continue
		}
		out = append(out, t)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// JudgmentStats summarises stored judgments for Diagnostics.
type JudgmentStats struct {
	Total        int            `json:"total"`
	ByProvider   map[string]int `json:"byProvider"`
	InputTokens  int64          `json:"inputTokens"`
	OutputTokens int64          `json:"outputTokens"`
}

func (db *DB) JudgmentStatsFor(ctx context.Context, questionsVersion string) (JudgmentStats, error) {
	st := JudgmentStats{ByProvider: map[string]int{}}
	rows, err := db.QueryContext(ctx, `SELECT provider, COUNT(*), COALESCE(SUM(json_extract(usage_json, '$.inputTokens')), 0), COALESCE(SUM(json_extract(usage_json, '$.outputTokens')), 0) FROM judgments WHERE questions_version = ? GROUP BY provider`, questionsVersion)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var n int
		var in, out int64
		if err := rows.Scan(&p, &n, &in, &out); err != nil {
			return st, err
		}
		st.ByProvider[p] = n
		st.Total += n
		st.InputTokens += in
		st.OutputTokens += out
	}
	return st, rows.Err()
}

// --- impact profiles ----------------------------------------------------------

// UserProfile is a user-authored profile row (YAML kept verbatim).
type UserProfile struct {
	ID        string    `json:"id"`
	YAML      string    `json:"yaml"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (db *DB) ListUserProfiles(ctx context.Context) ([]UserProfile, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, yaml, enabled, updated_at FROM profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserProfile
	for rows.Next() {
		var p UserProfile
		var en int
		var upd string
		if err := rows.Scan(&p.ID, &p.YAML, &en, &upd); err != nil {
			return nil, err
		}
		p.Enabled = en != 0
		p.UpdatedAt = parseTime(upd)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) PutUserProfile(ctx context.Context, id, yamlSrc string, enabled bool) error {
	_, err := db.ExecContext(ctx, `INSERT INTO profiles (id, yaml, enabled, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET yaml = excluded.yaml, enabled = excluded.enabled, updated_at = excluded.updated_at`, id, yamlSrc, boolInt(enabled), fmtTime(time.Now()))
	return err
}

func (db *DB) DeleteUserProfile(ctx context.Context, id string) error {
	res, err := db.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ProfileFlags returns enabled overrides for built-in profiles (absent = enabled).
func (db *DB) ProfileFlags(ctx context.Context) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, enabled FROM profile_flags`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var en int
		if err := rows.Scan(&id, &en); err != nil {
			return nil, err
		}
		out[id] = en != 0
	}
	return out, rows.Err()
}

func (db *DB) SetProfileEnabled(ctx context.Context, id string, enabled bool) error {
	if res, err := db.ExecContext(ctx, `UPDATE profiles SET enabled = ? WHERE id = ?`, boolInt(enabled), id); err != nil {
		return err
	} else if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, `INSERT INTO profile_flags (id, enabled) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled`, id, boolInt(enabled))
	return err
}

// --- PR impact analyses --------------------------------------------------------

// Analysis is one pull/merge request's impact analysis against a profile.
type Analysis struct {
	AccountID        int64           `json:"accountId"`
	Forge            string          `json:"forge"`
	Repo             string          `json:"repo"`
	Number           int             `json:"number"`
	Kind             string          `json:"kind"`
	HeadSHA          string          `json:"headSha"`
	ThreadVersion    string          `json:"threadVersion"`
	ProfileID        string          `json:"profileId"`
	Title            string          `json:"title"`
	HTMLURL          string          `json:"htmlUrl"`
	Author           string          `json:"author"`
	State            string          `json:"state"`
	MergedAt         *time.Time      `json:"mergedAt"`
	UpdatedAt        *time.Time      `json:"updatedAt"`
	ReportJSON       json.RawMessage `json:"report"`
	QuestionsVersion string          `json:"questionsVersion"`
	Provider         string          `json:"provider"`
	Model            string          `json:"model"`
	Calibrated       bool            `json:"calibrated"`
	AnswersJSON      json.RawMessage `json:"answers"`
	UsageJSON        json.RawMessage `json:"usage"`
	ImpactLevel      int             `json:"impactLevel"` // -1 unanalysed, 0 none … 3 certain
	ImpactScore      float64         `json:"impactScore"`
	ChangeKind       string          `json:"changeKind"`
	NoteJSON         json.RawMessage `json:"note"`
	NoteModel        string          `json:"noteModel"`
	Error            string          `json:"error"`
	AnalysedAt       *time.Time      `json:"analysedAt"`
	CreatedAt        time.Time       `json:"createdAt"`
}

// AnalysisKey identifies an analysis in maps.
func AnalysisKey(accountID int64, repo string, number int) string {
	return strconv.FormatInt(accountID, 10) + ":" + repo + "#" + strconv.Itoa(number)
}

const analysisColumns = `account_id, forge, repo, number, kind, head_sha, thread_version, profile_id, title, html_url, author, state, merged_at, updated_at,
	report_json, questions_version, provider, model, calibrated, answers_json, usage_json, impact_level, impact_score, change_kind, note_json, note_model, error, analysed_at, created_at`

func scanAnalysis(sc interface{ Scan(...any) error }) (Analysis, error) {
	var a Analysis
	var cal int
	var merged, updated, analysed sql.NullString
	var report, answers, usage, note, created string
	if err := sc.Scan(&a.AccountID, &a.Forge, &a.Repo, &a.Number, &a.Kind, &a.HeadSHA, &a.ThreadVersion, &a.ProfileID, &a.Title, &a.HTMLURL, &a.Author, &a.State, &merged, &updated,
		&report, &a.QuestionsVersion, &a.Provider, &a.Model, &cal, &answers, &usage, &a.ImpactLevel, &a.ImpactScore, &a.ChangeKind, &note, &a.NoteModel, &a.Error, &analysed, &created); err != nil {
		return Analysis{}, err
	}
	a.Calibrated = cal != 0
	a.MergedAt, a.UpdatedAt, a.AnalysedAt = parseTimePtr(merged), parseTimePtr(updated), parseTimePtr(analysed)
	a.ReportJSON, a.AnswersJSON, a.UsageJSON = json.RawMessage(report), json.RawMessage(answers), json.RawMessage(usage)
	if note != "" {
		a.NoteJSON = json.RawMessage(note)
	}
	a.CreatedAt = parseTime(created)
	return a, nil
}

func orJSON(r json.RawMessage, def string) string {
	if len(r) == 0 {
		return def
	}
	return string(r)
}

// PutAnalysis inserts or replaces the analysis row.
func (db *DB) PutAnalysis(ctx context.Context, a Analysis) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO pr_analysis (`+analysisColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id, repo, number) DO UPDATE SET forge = excluded.forge, kind = excluded.kind, head_sha = excluded.head_sha, thread_version = excluded.thread_version,
  profile_id = excluded.profile_id, title = excluded.title, html_url = excluded.html_url, author = excluded.author, state = excluded.state, merged_at = excluded.merged_at,
  updated_at = excluded.updated_at, report_json = excluded.report_json, questions_version = excluded.questions_version, provider = excluded.provider, model = excluded.model,
  calibrated = excluded.calibrated, answers_json = excluded.answers_json, usage_json = excluded.usage_json, impact_level = excluded.impact_level, impact_score = excluded.impact_score,
  change_kind = excluded.change_kind, note_json = excluded.note_json, note_model = excluded.note_model, error = excluded.error, analysed_at = excluded.analysed_at`,
		a.AccountID, a.Forge, a.Repo, a.Number, a.Kind, a.HeadSHA, a.ThreadVersion, a.ProfileID, a.Title, a.HTMLURL, a.Author, a.State, fmtTimePtr(a.MergedAt), fmtTimePtr(a.UpdatedAt),
		orJSON(a.ReportJSON, "{}"), a.QuestionsVersion, a.Provider, a.Model, boolInt(a.Calibrated), orJSON(a.AnswersJSON, "{}"), orJSON(a.UsageJSON, "{}"), a.ImpactLevel, a.ImpactScore,
		a.ChangeKind, orJSON(a.NoteJSON, ""), a.NoteModel, a.Error, fmtTimePtr(a.AnalysedAt), fmtTime(a.CreatedAt))
	return err
}

// SetAnalysisNote stores the generated impact note.
func (db *DB) SetAnalysisNote(ctx context.Context, accountID int64, repo string, number int, note json.RawMessage, model string) error {
	res, err := db.ExecContext(ctx, `UPDATE pr_analysis SET note_json = ?, note_model = ? WHERE account_id = ? AND repo = ? AND number = ?`, string(note), model, accountID, repo, number)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) GetAnalysis(ctx context.Context, accountID int64, repo string, number int) (Analysis, error) {
	a, err := scanAnalysis(db.QueryRowContext(ctx, `SELECT `+analysisColumns+` FROM pr_analysis WHERE account_id = ? AND repo = ? AND number = ?`, accountID, repo, number))
	if errors.Is(err, sql.ErrNoRows) {
		return Analysis{}, ErrNotFound
	}
	return a, err
}

// AnalysisQuery filters ListAnalyses.
type AnalysisQuery struct {
	AccountID int64
	MinLevel  int  // -1 includes unanalysed
	Landed    bool // true: merged only; false: open/closed (not merged)
	Any       bool // ignore Landed
	Limit     int
}

// ListAnalyses returns analyses newest-first (by updated_at), filtered.
func (db *DB) ListAnalyses(ctx context.Context, q AnalysisQuery) ([]Analysis, error) {
	where := []string{"impact_level >= ?"}
	args := []any{q.MinLevel}
	if q.AccountID != 0 {
		where = append(where, "account_id = ?")
		args = append(args, q.AccountID)
	}
	if !q.Any {
		if q.Landed {
			where = append(where, "state = 'merged'")
		} else {
			where = append(where, "state <> 'merged'")
		}
	}
	sqlStr := `SELECT ` + analysisColumns + ` FROM pr_analysis WHERE ` + strings.Join(where, " AND ") + ` ORDER BY impact_level DESC, COALESCE(updated_at, created_at) DESC`
	if q.Limit > 0 {
		sqlStr += " LIMIT " + strconv.Itoa(q.Limit)
	}
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Analysis
	for rows.Next() {
		a, err := scanAnalysis(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AnalysesByKey returns all analyses for an account (0 = all) keyed by AnalysisKey.
func (db *DB) AnalysesByKey(ctx context.Context, accountID int64) (map[string]Analysis, error) {
	list, err := db.ListAnalyses(ctx, AnalysisQuery{AccountID: accountID, MinLevel: -1, Any: true})
	if err != nil {
		return nil, err
	}
	out := make(map[string]Analysis, len(list))
	for _, a := range list {
		out[AnalysisKey(a.AccountID, a.Repo, a.Number)] = a
	}
	return out, nil
}

// --- summaries & digests (M4) ------------------------------------------------

// Summary is a stored thread summary for one thread version.
type Summary struct {
	AccountID     int64           `json:"accountId"`
	ThreadID      string          `json:"threadId"`
	ThreadVersion string          `json:"threadVersion"`
	Model         string          `json:"model"`
	ContentJSON   json.RawMessage `json:"content"`
	UsageJSON     json.RawMessage `json:"usage"`
	LatencyMs     int64           `json:"latencyMs"`
	CreatedAt     time.Time       `json:"createdAt"`
}

func (db *DB) PutSummary(ctx context.Context, s Summary) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO summaries (account_id, thread_id, thread_version, model, content_json, usage_json, latency_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id, thread_id) DO UPDATE SET thread_version = excluded.thread_version, model = excluded.model, content_json = excluded.content_json,
  usage_json = excluded.usage_json, latency_ms = excluded.latency_ms, created_at = excluded.created_at`,
		s.AccountID, s.ThreadID, s.ThreadVersion, s.Model, orJSON(s.ContentJSON, "{}"), orJSON(s.UsageJSON, "{}"), s.LatencyMs, fmtTime(time.Now()))
	return err
}

func scanSummary(sc interface{ Scan(...any) error }) (Summary, error) {
	var s Summary
	var content, usage, created string
	if err := sc.Scan(&s.AccountID, &s.ThreadID, &s.ThreadVersion, &s.Model, &content, &usage, &s.LatencyMs, &created); err != nil {
		return Summary{}, err
	}
	s.ContentJSON, s.UsageJSON, s.CreatedAt = json.RawMessage(content), json.RawMessage(usage), parseTime(created)
	return s, nil
}

const summaryColumns = `account_id, thread_id, thread_version, model, content_json, usage_json, latency_ms, created_at`

func (db *DB) GetSummary(ctx context.Context, accountID int64, threadID string) (Summary, error) {
	s, err := scanSummary(db.QueryRowContext(ctx, `SELECT `+summaryColumns+` FROM summaries WHERE account_id = ? AND thread_id = ?`, accountID, threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, ErrNotFound
	}
	return s, err
}

// SummariesByKey returns all summaries keyed by JudgmentKey (accountID 0 = all).
func (db *DB) SummariesByKey(ctx context.Context, accountID int64) (map[string]Summary, error) {
	q := `SELECT ` + summaryColumns + ` FROM summaries`
	var args []any
	if accountID != 0 {
		q += " WHERE account_id = ?"
		args = append(args, accountID)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Summary{}
	for rows.Next() {
		s, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out[JudgmentKey(s.AccountID, s.ThreadID)] = s
	}
	return out, rows.Err()
}

// Digest is a stored period overview.
type Digest struct {
	ID          int64           `json:"id"`
	PeriodStart time.Time       `json:"periodStart"`
	PeriodEnd   time.Time       `json:"periodEnd"`
	Model       string          `json:"model"`
	ContentJSON json.RawMessage `json:"content"`
	UsageJSON   json.RawMessage `json:"usage"`
	LatencyMs   int64           `json:"latencyMs"`
	ThreadCount int             `json:"threadCount"`
	CreatedAt   time.Time       `json:"createdAt"`
}

func (db *DB) PutDigest(ctx context.Context, d Digest) (int64, error) {
	res, err := db.ExecContext(ctx, `INSERT INTO digests (period_start, period_end, model, content_json, usage_json, latency_ms, thread_count, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		fmtTime(d.PeriodStart), fmtTime(d.PeriodEnd), d.Model, orJSON(d.ContentJSON, "{}"), orJSON(d.UsageJSON, "{}"), d.LatencyMs, d.ThreadCount, fmtTime(time.Now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) ListDigests(ctx context.Context, limit int) ([]Digest, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.QueryContext(ctx, `SELECT id, period_start, period_end, model, content_json, usage_json, latency_ms, thread_count, created_at FROM digests ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Digest
	for rows.Next() {
		var d Digest
		var ps, pe, content, usage, created string
		if err := rows.Scan(&d.ID, &ps, &pe, &d.Model, &content, &usage, &d.LatencyMs, &d.ThreadCount, &created); err != nil {
			return nil, err
		}
		d.PeriodStart, d.PeriodEnd, d.CreatedAt = parseTime(ps), parseTime(pe), parseTime(created)
		d.ContentJSON, d.UsageJSON = json.RawMessage(content), json.RawMessage(usage)
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkNotified records a delivered notification; false when it was already recorded.
func (db *DB) MarkNotified(ctx context.Context, key string) (bool, error) {
	res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO notified (key, created_at) VALUES (?, ?)`, key, fmtTime(time.Now()))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UsageRow aggregates model usage for one (source, provider, model).
type UsageRow struct {
	Source       string  `json:"source"` // judgments | analyses | summaries | digests
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Count        int     `json:"count"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	AvgLatencyMs float64 `json:"avgLatencyMs"`
}

// UsageStats sums token usage and latency across every stored generation.
func (db *DB) UsageStats(ctx context.Context) ([]UsageRow, error) {
	q := `
SELECT 'judgments', provider, model, COUNT(*), COALESCE(SUM(json_extract(usage_json,'$.inputTokens')),0), COALESCE(SUM(json_extract(usage_json,'$.outputTokens')),0), COALESCE(AVG(latency_ms),0) FROM judgments GROUP BY provider, model
UNION ALL
SELECT 'analyses', provider, model, COUNT(*), COALESCE(SUM(json_extract(usage_json,'$.inputTokens')),0), COALESCE(SUM(json_extract(usage_json,'$.outputTokens')),0), 0 FROM pr_analysis WHERE impact_level >= 0 GROUP BY provider, model
UNION ALL
SELECT 'summaries', 'ollama', model, COUNT(*), COALESCE(SUM(json_extract(usage_json,'$.inputTokens')),0), COALESCE(SUM(json_extract(usage_json,'$.outputTokens')),0), COALESCE(AVG(latency_ms),0) FROM summaries GROUP BY model
UNION ALL
SELECT 'digests', 'ollama', model, COUNT(*), COALESCE(SUM(json_extract(usage_json,'$.inputTokens')),0), COALESCE(SUM(json_extract(usage_json,'$.outputTokens')),0), COALESCE(AVG(latency_ms),0) FROM digests GROUP BY model`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Source, &r.Provider, &r.Model, &r.Count, &r.InputTokens, &r.OutputTokens, &r.AvgLatencyMs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- labels & evaluation (M5) ------------------------------------------------

// Label is the user's ground truth for one thread. Pointer/−1 fields are "unset".
type Label struct {
	AccountID      int64     `json:"accountId"`
	ThreadID       string    `json:"threadId"`
	Category       string    `json:"category"`
	RequiresAction *bool     `json:"requiresAction"`
	Urgency        int       `json:"urgency"`
	Relevance      int       `json:"relevance"`
	Priority       int       `json:"priority"`
	Resolved       *bool     `json:"resolved"`
	Noise          *bool     `json:"noise"`
	Note           string    `json:"note"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func nullBool(b *bool) any {
	if b == nil {
		return nil
	}
	return boolInt(*b)
}

func boolPtr(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 != 0
	return &v
}

func (db *DB) PutLabel(ctx context.Context, l Label) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO labels (account_id, thread_id, category, requires_action, urgency, relevance, priority, resolved, noise, note, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id, thread_id) DO UPDATE SET category = excluded.category, requires_action = excluded.requires_action, urgency = excluded.urgency,
  relevance = excluded.relevance, priority = excluded.priority, resolved = excluded.resolved, noise = excluded.noise, note = excluded.note, updated_at = excluded.updated_at`,
		l.AccountID, l.ThreadID, l.Category, nullBool(l.RequiresAction), l.Urgency, l.Relevance, l.Priority, nullBool(l.Resolved), nullBool(l.Noise), l.Note, fmtTime(time.Now()))
	return err
}

func (db *DB) DeleteLabel(ctx context.Context, accountID int64, threadID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM labels WHERE account_id = ? AND thread_id = ?`, accountID, threadID)
	return err
}

const labelColumns = `account_id, thread_id, category, requires_action, urgency, relevance, priority, resolved, noise, note, updated_at`

func scanLabel(sc interface{ Scan(...any) error }) (Label, error) {
	var l Label
	var ra, res, noise sql.NullInt64
	var upd string
	if err := sc.Scan(&l.AccountID, &l.ThreadID, &l.Category, &ra, &l.Urgency, &l.Relevance, &l.Priority, &res, &noise, &l.Note, &upd); err != nil {
		return Label{}, err
	}
	l.RequiresAction, l.Resolved, l.Noise = boolPtr(ra), boolPtr(res), boolPtr(noise)
	l.UpdatedAt = parseTime(upd)
	return l, nil
}

func (db *DB) GetLabel(ctx context.Context, accountID int64, threadID string) (Label, error) {
	l, err := scanLabel(db.QueryRowContext(ctx, `SELECT `+labelColumns+` FROM labels WHERE account_id = ? AND thread_id = ?`, accountID, threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return Label{}, ErrNotFound
	}
	return l, err
}

// ListLabels returns labels keyed by JudgmentKey (accountID 0 = all).
func (db *DB) ListLabels(ctx context.Context, accountID int64) (map[string]Label, error) {
	q := `SELECT ` + labelColumns + ` FROM labels`
	var args []any
	if accountID != 0 {
		q += " WHERE account_id = ?"
		args = append(args, accountID)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Label{}
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out[JudgmentKey(l.AccountID, l.ThreadID)] = l
	}
	return out, rows.Err()
}

// PRLabel is the user's ground truth for one pull/merge request's impact.
type PRLabel struct {
	AccountID   int64     `json:"accountId"`
	Repo        string    `json:"repo"`
	Number      int       `json:"number"`
	ImpactLevel int       `json:"impactLevel"`
	ChangeKind  string    `json:"changeKind"`
	Note        string    `json:"note"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (db *DB) PutPRLabel(ctx context.Context, l PRLabel) error {
	_, err := db.ExecContext(ctx, `INSERT INTO pr_labels (account_id, repo, number, impact_level, change_kind, note, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id, repo, number) DO UPDATE SET impact_level = excluded.impact_level, change_kind = excluded.change_kind, note = excluded.note, updated_at = excluded.updated_at`,
		l.AccountID, l.Repo, l.Number, l.ImpactLevel, l.ChangeKind, l.Note, fmtTime(time.Now()))
	return err
}

// ListPRLabels returns PR labels keyed by AnalysisKey.
func (db *DB) ListPRLabels(ctx context.Context) (map[string]PRLabel, error) {
	rows, err := db.QueryContext(ctx, `SELECT account_id, repo, number, impact_level, change_kind, note, updated_at FROM pr_labels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PRLabel{}
	for rows.Next() {
		var l PRLabel
		var upd string
		if err := rows.Scan(&l.AccountID, &l.Repo, &l.Number, &l.ImpactLevel, &l.ChangeKind, &l.Note, &upd); err != nil {
			return nil, err
		}
		l.UpdatedAt = parseTime(upd)
		out[AnalysisKey(l.AccountID, l.Repo, l.Number)] = l
	}
	return out, rows.Err()
}

// EvalJudgment is a provider-specific judgment kept for evaluation only.
type EvalJudgment struct {
	AccountID     int64           `json:"accountId"`
	ThreadID      string          `json:"threadId"`
	Provider      string          `json:"provider"`
	Model         string          `json:"model"`
	Calibrated    bool            `json:"calibrated"`
	ThreadVersion string          `json:"threadVersion"`
	AnswersJSON   json.RawMessage `json:"answers"`
	UsageJSON     json.RawMessage `json:"usage"`
	LatencyMs     int64           `json:"latencyMs"`
	CreatedAt     time.Time       `json:"createdAt"`
}

func (db *DB) PutEvalJudgment(ctx context.Context, j EvalJudgment) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO eval_judgments (account_id, thread_id, provider, model, calibrated, thread_version, answers_json, usage_json, latency_ms, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id, thread_id, provider) DO UPDATE SET model = excluded.model, calibrated = excluded.calibrated, thread_version = excluded.thread_version,
  answers_json = excluded.answers_json, usage_json = excluded.usage_json, latency_ms = excluded.latency_ms, created_at = excluded.created_at`,
		j.AccountID, j.ThreadID, j.Provider, j.Model, boolInt(j.Calibrated), j.ThreadVersion, orJSON(j.AnswersJSON, "{}"), orJSON(j.UsageJSON, "{}"), j.LatencyMs, fmtTime(time.Now()))
	return err
}

// ListEvalJudgments returns one provider's eval judgments keyed by JudgmentKey.
func (db *DB) ListEvalJudgments(ctx context.Context, provider string) (map[string]EvalJudgment, error) {
	rows, err := db.QueryContext(ctx, `SELECT account_id, thread_id, provider, model, calibrated, thread_version, answers_json, usage_json, latency_ms, created_at FROM eval_judgments WHERE provider = ?`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]EvalJudgment{}
	for rows.Next() {
		var j EvalJudgment
		var cal int
		var answers, usage, created string
		if err := rows.Scan(&j.AccountID, &j.ThreadID, &j.Provider, &j.Model, &cal, &j.ThreadVersion, &answers, &usage, &j.LatencyMs, &created); err != nil {
			return nil, err
		}
		j.Calibrated = cal != 0
		j.AnswersJSON, j.UsageJSON, j.CreatedAt = json.RawMessage(answers), json.RawMessage(usage), parseTime(created)
		out[JudgmentKey(j.AccountID, j.ThreadID)] = j
	}
	return out, rows.Err()
}

// EvalProviders lists providers present in eval_judgments with counts.
func (db *DB) EvalProviders(ctx context.Context) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT provider, COUNT(*) FROM eval_judgments GROUP BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var p string
		var n int
		if err := rows.Scan(&p, &n); err != nil {
			return nil, err
		}
		out[p] = n
	}
	return out, rows.Err()
}

// --- watched projects (GitLab) ---------------------------------------------

// WatchedProject is a project whose activity is polled for an account.
type WatchedProject struct {
	AccountID   int64      `json:"accountId"`
	Path        string     `json:"path"`
	ProjectID   int64      `json:"projectId"`
	LastEventAt *time.Time `json:"lastEventAt"`
	LastError   string     `json:"lastError"`
	CreatedAt   time.Time  `json:"createdAt"`
}

func (db *DB) ListWatched(ctx context.Context, accountID int64) ([]WatchedProject, error) {
	rows, err := db.QueryContext(ctx, `SELECT account_id, path, project_id, last_event_at, last_error, created_at FROM watched_projects WHERE account_id = ? ORDER BY path`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WatchedProject
	for rows.Next() {
		var w WatchedProject
		var last sql.NullString
		var created string
		if err := rows.Scan(&w.AccountID, &w.Path, &w.ProjectID, &last, &w.LastError, &created); err != nil {
			return nil, err
		}
		w.LastEventAt = parseTimePtr(last)
		w.CreatedAt = parseTime(created)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (db *DB) AddWatched(ctx context.Context, accountID int64, path string) error {
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return errors.New("store: empty project path")
	}
	_, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO watched_projects (account_id, path, created_at) VALUES (?, ?, ?)`, accountID, path, fmtTime(time.Now()))
	return err
}

func (db *DB) RemoveWatched(ctx context.Context, accountID int64, path string) error {
	res, err := db.ExecContext(ctx, `DELETE FROM watched_projects WHERE account_id = ? AND path = ?`, accountID, path)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateWatched stores the resolved project id, the newest event time seen, and the last error.
func (db *DB) UpdateWatched(ctx context.Context, accountID int64, path string, projectID int64, lastEventAt *time.Time, lastErr string) error {
	_, err := db.ExecContext(ctx, `UPDATE watched_projects SET project_id = ?, last_event_at = COALESCE(?, last_event_at), last_error = ? WHERE account_id = ? AND path = ?`,
		projectID, fmtTimePtr(lastEventAt), lastErr, accountID, path)
	return err
}

// --- items ("mine") ---------------------------------------------------------

// Item is an issue/PR related to the account (assigned, mentioned, review-requested, author).
type Item struct {
	AccountID int64     `json:"accountId"`
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Kind      string    `json:"kind"` // issue | pr
	Title     string    `json:"title"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"htmlUrl"`
	Author    string    `json:"author"`
	Assignees []string  `json:"assignees"`
	Labels    []string  `json:"labels"`
	Draft     bool      `json:"draft"`
	Comments  int       `json:"comments"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Relations []string  `json:"relations"`
}

func (db *DB) UpsertItem(ctx context.Context, it Item, run int64) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO items (account_id, repo, number, kind, title, state, html_url, author, assignees, labels, draft, comments, created_at, updated_at, relations, last_seen_run)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id, repo, number) DO UPDATE SET
  kind = excluded.kind, title = excluded.title, state = excluded.state, html_url = excluded.html_url,
  author = excluded.author, assignees = excluded.assignees, labels = excluded.labels, draft = excluded.draft,
  comments = excluded.comments, created_at = excluded.created_at, updated_at = excluded.updated_at,
  relations = excluded.relations, last_seen_run = excluded.last_seen_run`,
		it.AccountID, it.Repo, it.Number, it.Kind, it.Title, it.State, it.HTMLURL, it.Author, toJSON(it.Assignees), toJSON(it.Labels),
		boolInt(it.Draft), it.Comments, fmtTime(it.CreatedAt), fmtTime(it.UpdatedAt), toJSON(it.Relations), run)
	return err
}

// PruneItems removes items for the account that were not seen in the given run.
func (db *DB) PruneItems(ctx context.Context, accountID, run int64) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM items WHERE account_id = ? AND last_seen_run <> ?`, accountID, run)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (db *DB) ListItems(ctx context.Context, accountID int64, includeClosed bool) ([]Item, error) {
	var where []string
	var args []any
	if accountID != 0 {
		where = append(where, "account_id = ?")
		args = append(args, accountID)
	}
	if !includeClosed {
		where = append(where, "state = 'open'")
	}
	sqlStr := `SELECT account_id, repo, number, kind, title, state, html_url, author, assignees, labels, draft, comments, created_at, updated_at, relations FROM items`
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlStr += " ORDER BY updated_at DESC"
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var assignees, labels, relations, created, updated string
		var draft int
		if err := rows.Scan(&it.AccountID, &it.Repo, &it.Number, &it.Kind, &it.Title, &it.State, &it.HTMLURL, &it.Author, &assignees, &labels, &draft, &it.Comments, &created, &updated, &relations); err != nil {
			return nil, err
		}
		it.Assignees = fromJSONStrings(assignees)
		it.Labels = fromJSONStrings(labels)
		it.Relations = fromJSONStrings(relations)
		it.Draft = draft != 0
		it.CreatedAt = parseTime(created)
		it.UpdatedAt = parseTime(updated)
		out = append(out, it)
	}
	return out, rows.Err()
}

// --- sync state -------------------------------------------------------------

type SyncState struct {
	AccountID       int64      `json:"accountId"`
	LastSince       *time.Time `json:"lastSince"`
	LastFullAt      *time.Time `json:"lastFullAt"`
	LastSyncAt      *time.Time `json:"lastSyncAt"`
	LastMineAt      *time.Time `json:"lastMineAt"`
	MineRun         int64      `json:"mineRun"`
	PollIntervalSec int        `json:"pollIntervalSec"`
	LastError       string     `json:"lastError"`
	LastLandedScan  *time.Time `json:"lastLandedScan"`
}

func (db *DB) GetSyncState(ctx context.Context, accountID int64) (SyncState, error) {
	var s SyncState
	var since, full, sync, mine, landed sql.NullString
	err := db.QueryRowContext(ctx, `SELECT account_id, last_since, last_full_at, last_sync_at, last_mine_at, mine_run, poll_interval_sec, last_error, last_landed_scan_at FROM sync_state WHERE account_id = ?`, accountID).
		Scan(&s.AccountID, &since, &full, &sync, &mine, &s.MineRun, &s.PollIntervalSec, &s.LastError, &landed)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncState{AccountID: accountID, PollIntervalSec: 180}, nil
	}
	if err != nil {
		return SyncState{}, err
	}
	s.LastSince, s.LastFullAt, s.LastSyncAt, s.LastMineAt, s.LastLandedScan = parseTimePtr(since), parseTimePtr(full), parseTimePtr(sync), parseTimePtr(mine), parseTimePtr(landed)
	return s, nil
}

func (db *DB) PutSyncState(ctx context.Context, s SyncState) error {
	if s.PollIntervalSec <= 0 {
		s.PollIntervalSec = 180
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO sync_state (account_id, last_since, last_full_at, last_sync_at, last_mine_at, mine_run, poll_interval_sec, last_error, last_landed_scan_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id) DO UPDATE SET last_since = excluded.last_since, last_full_at = excluded.last_full_at,
  last_sync_at = excluded.last_sync_at, last_mine_at = excluded.last_mine_at, mine_run = excluded.mine_run,
  poll_interval_sec = excluded.poll_interval_sec, last_error = excluded.last_error, last_landed_scan_at = excluded.last_landed_scan_at`,
		s.AccountID, fmtTimePtr(s.LastSince), fmtTimePtr(s.LastFullAt), fmtTimePtr(s.LastSyncAt), fmtTimePtr(s.LastMineAt), s.MineRun, s.PollIntervalSec, s.LastError, fmtTimePtr(s.LastLandedScan))
	return err
}

// --- settings ---------------------------------------------------------------

// GetSetting decodes the JSON value stored under key into v. Missing keys leave v untouched and return false.
func (db *DB) GetSetting(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

func (db *DB) SetSetting(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO settings (key, value_json) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value_json = excluded.value_json`, key, string(raw))
	return err
}

// Counts summarises the inbox for badges.
type Counts struct {
	Unread int `json:"unread"`
	Noise  int `json:"noise"`
	Done   int `json:"done"`
	Items  int `json:"items"`
}

func (db *DB) Counts(ctx context.Context, accountID int64) (Counts, error) {
	var c Counts
	acct := ""
	var args []any
	if accountID != 0 {
		acct = " AND account_id = ?"
		args = append(args, accountID)
	}
	now := fmtTime(time.Now())
	q := `SELECT
  (SELECT COUNT(*) FROM threads WHERE unread = 1 AND local_read_at IS NULL AND done_at IS NULL AND filter_verdict = 'keep' AND (snoozed_until IS NULL OR snoozed_until <= '` + now + `')` + acct + `),
  (SELECT COUNT(*) FROM threads WHERE filter_verdict = 'noise' AND done_at IS NULL` + acct + `),
  (SELECT COUNT(*) FROM threads WHERE done_at IS NOT NULL` + acct + `),
  (SELECT COUNT(*) FROM items WHERE state = 'open'` + acct + `)`
	all := append(append(append(append([]any{}, args...), args...), args...), args...)
	if err := db.QueryRowContext(ctx, q, all...).Scan(&c.Unread, &c.Noise, &c.Done, &c.Items); err != nil {
		return Counts{}, err
	}
	return c, nil
}
