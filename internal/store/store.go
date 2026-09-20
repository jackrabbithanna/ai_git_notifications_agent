// Package store is the app's SQLite persistence layer (pure-Go driver).
package store

import (
	"context"
	"database/sql"
	"embed"
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

// DefaultPath is $XDG_DATA_HOME/ghinbox/ghinbox.db (or ~/.local/share/...).
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "ghinbox", "ghinbox.db"), nil
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
	ID        int64     `json:"id"`
	Login     string    `json:"login"`
	Host      string    `json:"host"`
	WriteMode string    `json:"writeMode"`
	CreatedAt time.Time `json:"createdAt"`
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

func (db *DB) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, login, host, write_mode, created_at FROM accounts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		var created string
		if err := rows.Scan(&a.ID, &a.Login, &a.Host, &a.WriteMode, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (db *DB) GetAccount(ctx context.Context, id int64) (Account, error) {
	var a Account
	var created string
	err := db.QueryRowContext(ctx, `SELECT id, login, host, write_mode, created_at FROM accounts WHERE id = ?`, id).
		Scan(&a.ID, &a.Login, &a.Host, &a.WriteMode, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	a.CreatedAt = parseTime(created)
	return a, nil
}

// FindAccount looks an account up by login (and host, "" meaning github.com).
func (db *DB) FindAccount(ctx context.Context, login, host string) (Account, error) {
	if host == "" {
		host = "github.com"
	}
	var a Account
	var created string
	err := db.QueryRowContext(ctx, `SELECT id, login, host, write_mode, created_at FROM accounts WHERE login = ? AND host = ?`, login, host).
		Scan(&a.ID, &a.Login, &a.Host, &a.WriteMode, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	a.CreatedAt = parseTime(created)
	return a, nil
}

func (db *DB) InsertAccount(ctx context.Context, login, host string) (Account, error) {
	if host == "" {
		host = "github.com"
	}
	now := time.Now()
	res, err := db.ExecContext(ctx, `INSERT INTO accounts (login, host, write_mode, created_at) VALUES (?, ?, 'readonly', ?)`, login, host, fmtTime(now))
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
	return Account{ID: id, Login: login, Host: host, WriteMode: "readonly", CreatedAt: now.UTC()}, nil
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
}

// IsRead is true when GitHub or the user marked the thread read.
func (t Thread) IsRead() bool { return !t.Unread || t.LocalReadAt != nil }

const threadColumns = `account_id, thread_id, repo, subject_type, subject_url, subject_number, html_url, title, reason,
	unread, updated_at, last_read_at, latest_comment_url, activity_kind, relation_tags, filter_verdict, filter_reason,
	local_read_at, done_at, snoozed_until, first_seen_at, last_synced_at`

func scanThread(sc interface{ Scan(...any) error }) (Thread, error) {
	var t Thread
	var unread int
	var updated, tags, firstSeen, lastSynced string
	var lastRead, localRead, done, snoozed sql.NullString
	err := sc.Scan(&t.AccountID, &t.ThreadID, &t.Repo, &t.SubjectType, &t.SubjectURL, &t.SubjectNumber, &t.HTMLURL, &t.Title, &t.Reason,
		&unread, &updated, &lastRead, &t.LatestCommentURL, &t.ActivityKind, &tags, &t.FilterVerdict, &t.FilterReason,
		&localRead, &done, &snoozed, &firstSeen, &lastSynced)
	if err != nil {
		return Thread{}, err
	}
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
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?)
ON CONFLICT (account_id, thread_id) DO UPDATE SET
  repo = excluded.repo, subject_type = excluded.subject_type, subject_url = excluded.subject_url,
  subject_number = excluded.subject_number, html_url = excluded.html_url, title = excluded.title,
  reason = excluded.reason, unread = excluded.unread, updated_at = excluded.updated_at,
  last_read_at = excluded.last_read_at, latest_comment_url = excluded.latest_comment_url,
  activity_kind = excluded.activity_kind, relation_tags = excluded.relation_tags,
  filter_verdict = excluded.filter_verdict, filter_reason = excluded.filter_reason,
  local_read_at = CASE WHEN excluded.updated_at > threads.updated_at THEN NULL ELSE threads.local_read_at END,
  done_at       = CASE WHEN excluded.updated_at > threads.updated_at THEN NULL ELSE threads.done_at END,
  snoozed_until = CASE WHEN excluded.updated_at > threads.updated_at THEN NULL ELSE threads.snoozed_until END,
  last_synced_at = excluded.last_synced_at`,
		t.AccountID, t.ThreadID, t.Repo, t.SubjectType, t.SubjectURL, t.SubjectNumber, t.HTMLURL, t.Title, t.Reason,
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
}

func (db *DB) GetSyncState(ctx context.Context, accountID int64) (SyncState, error) {
	var s SyncState
	var since, full, sync, mine sql.NullString
	err := db.QueryRowContext(ctx, `SELECT account_id, last_since, last_full_at, last_sync_at, last_mine_at, mine_run, poll_interval_sec, last_error FROM sync_state WHERE account_id = ?`, accountID).
		Scan(&s.AccountID, &since, &full, &sync, &mine, &s.MineRun, &s.PollIntervalSec, &s.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncState{AccountID: accountID, PollIntervalSec: 180}, nil
	}
	if err != nil {
		return SyncState{}, err
	}
	s.LastSince, s.LastFullAt, s.LastSyncAt, s.LastMineAt = parseTimePtr(since), parseTimePtr(full), parseTimePtr(sync), parseTimePtr(mine)
	return s, nil
}

func (db *DB) PutSyncState(ctx context.Context, s SyncState) error {
	if s.PollIntervalSec <= 0 {
		s.PollIntervalSec = 180
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO sync_state (account_id, last_since, last_full_at, last_sync_at, last_mine_at, mine_run, poll_interval_sec, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id) DO UPDATE SET last_since = excluded.last_since, last_full_at = excluded.last_full_at,
  last_sync_at = excluded.last_sync_at, last_mine_at = excluded.last_mine_at, mine_run = excluded.mine_run,
  poll_interval_sec = excluded.poll_interval_sec, last_error = excluded.last_error`,
		s.AccountID, fmtTimePtr(s.LastSince), fmtTimePtr(s.LastFullAt), fmtTimePtr(s.LastSyncAt), fmtTimePtr(s.LastMineAt), s.MineRun, s.PollIntervalSec, s.LastError)
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
