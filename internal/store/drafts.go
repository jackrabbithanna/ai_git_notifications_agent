package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Draft is reply text the agent drafted for a thread. It is stored locally
// and never posted to a forge (M6).
type Draft struct {
	AccountID int64     `json:"accountId"`
	ThreadID  string    `json:"threadId"`
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Text      string    `json:"text"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"createdAt"`
}

const draftColumns = `account_id, thread_id, repo, number, text, model, created_at`

func (db *DB) PutDraft(ctx context.Context, d Draft) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	_, err := db.ExecContext(ctx, `
INSERT INTO drafts (`+draftColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (account_id, thread_id) DO UPDATE SET repo = excluded.repo, number = excluded.number, text = excluded.text,
  model = excluded.model, created_at = excluded.created_at`,
		d.AccountID, d.ThreadID, d.Repo, d.Number, d.Text, d.Model, fmtTime(d.CreatedAt))
	return err
}

func scanDraft(sc interface{ Scan(...any) error }) (Draft, error) {
	var d Draft
	var created string
	if err := sc.Scan(&d.AccountID, &d.ThreadID, &d.Repo, &d.Number, &d.Text, &d.Model, &created); err != nil {
		return Draft{}, err
	}
	d.CreatedAt = parseTime(created)
	return d, nil
}

// GetDraft returns the draft for a thread (ErrNotFound when none).
func (db *DB) GetDraft(ctx context.Context, accountID int64, threadID string) (Draft, error) {
	d, err := scanDraft(db.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM drafts WHERE account_id = ? AND thread_id = ?`, accountID, threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	return d, err
}

func (db *DB) DeleteDraft(ctx context.Context, accountID int64, threadID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM drafts WHERE account_id = ? AND thread_id = ?`, accountID, threadID)
	return err
}

// ListDrafts returns drafts newest-first (accountID 0 = all accounts).
func (db *DB) ListDrafts(ctx context.Context, accountID int64) ([]Draft, error) {
	q := `SELECT ` + draftColumns + ` FROM drafts`
	var args []any
	if accountID != 0 {
		q += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	rows, err := db.QueryContext(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Draft
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
