// Package source defines the seam between the pipeline and a forge (GitHub,
// GitLab). A Source turns forge-specific feeds into classified store.Thread and
// store.Item rows; the pipeline owns filtering, persistence and scheduling.
package source

import (
	"context"
	"errors"
	"time"

	"ghinbox/internal/store"
)

// ErrWritesDisabled is returned by write methods while the account is read-only.
var ErrWritesDisabled = errors.New("source: GitHub/GitLab writes are disabled for this account (write mode readonly)")

// ErrNotMirrorable means the forge has no equivalent state for this action; the
// local change stands alone and no warning is warranted.
var ErrNotMirrorable = errors.New("source: action has no forge-side equivalent")

// Opts parameterise one notifications fetch.
type Opts struct {
	AccountID int64
	Host      string
	Full      bool      // re-read a wider window including read/done items
	Since     time.Time // incremental lower bound (zero = forge default)
}

// ReadExcept asks the pipeline to mark stored threads with the prefix as read
// unless their id is listed — how GitLab reconciles to-dos completed elsewhere.
type ReadExcept struct {
	Prefix string
	IDs    []string
}

// Result is the outcome of Notifications.
type Result struct {
	Threads    []store.Thread
	ReadExcept *ReadExcept
	Warnings   []string // non-fatal problems (e.g. one watched project failed)
}

// Source is one account's connection to its forge.
type Source interface {
	Forge() string
	// Login returns the login behind the token; used when adding an account.
	Login(ctx context.Context) (string, error)
	// Notifications returns classified (not yet filtered) threads, all pages.
	Notifications(ctx context.Context, opts Opts) (Result, error)
	// Mine returns issues/PRs/MRs related to the account with merged relations.
	Mine(ctx context.Context) ([]store.Item, error)
	MarkRead(ctx context.Context, threadID string) error
	MarkDone(ctx context.Context, threadID string) error
	Unsubscribe(ctx context.Context, threadID string) error
	Close() error
}

// WatchUpdate is what a Watcher learned about one watched project.
type WatchUpdate struct {
	Path        string
	ProjectID   int64
	LastEventAt *time.Time // nil keeps the stored value
	Err         string
}

// Watcher is implemented by sources that poll configured projects for activity
// (GitLab). The pipeline persists the updates and stores the threads.
type Watcher interface {
	SyncWatched(ctx context.Context, watched []store.WatchedProject, accountID int64) ([]store.Thread, []WatchUpdate, []string, error)
}

// Enricher is implemented by sources that can fetch an item's body/state and
// the latest activity for a thread (input to judgments). Implementations keep
// it to one or two requests per thread.
type Enricher interface {
	Enrich(ctx context.Context, t store.Thread) (store.Enrichment, error)
}

// TrimText caps a body for model state (bytes, at a rune boundary).
func TrimText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// ScopeReporter is implemented by sources that can learn the token's scopes.
type ScopeReporter interface {
	TokenScopes(ctx context.Context) (string, error) // comma list; "" when unknown
}

// AppendUnique appends v to s unless already present (relation merging helper).
func AppendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
