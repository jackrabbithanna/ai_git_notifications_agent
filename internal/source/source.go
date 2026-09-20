// Package source defines the seam between the pipeline and a forge (GitHub,
// GitLab). A Source turns forge-specific feeds into classified store.Thread and
// store.Item rows; the pipeline owns filtering, persistence and scheduling.
package source

import (
	"context"
	"errors"
	"time"

	"gitinbox/internal/profiles"
	"gitinbox/internal/store"
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

// ChangeSet is a pull/merge request with its changed files (PLAN.md §4.4).
type ChangeSet struct {
	Forge      string                `json:"forge"`
	Repo       string                `json:"repo"`
	Number     int                   `json:"number"`
	Kind       string                `json:"kind"` // pr | mr
	Title      string                `json:"title"`
	Body       string                `json:"body"`
	Labels     []string              `json:"labels"`
	Base       string                `json:"base"`
	State      string                `json:"state"` // open | merged | closed
	Draft      bool                  `json:"draft"`
	Author     string                `json:"author"`
	HeadSHA    string                `json:"headSha"`
	HTMLURL    string                `json:"htmlUrl"`
	CreatedAt  time.Time             `json:"createdAt"`
	UpdatedAt  time.Time             `json:"updatedAt"`
	MergedAt   *time.Time            `json:"mergedAt"`
	Files      []profiles.FileChange `json:"files"`
	FilesTotal int                   `json:"filesTotal"`
	Additions  int                   `json:"additions"`
	Deletions  int                   `json:"deletions"`
	Truncated  bool                  `json:"truncated"` // file list or patches were capped
}

// Changer fetches a pull/merge request's metadata and changed files.
type Changer interface {
	Changes(ctx context.Context, repo string, number int) (ChangeSet, error)
}

// ChangeRef points at a pull/merge request without its files.
type ChangeRef struct {
	Repo      string     `json:"repo"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	HTMLURL   string     `json:"htmlUrl"`
	Author    string     `json:"author"`
	MergedAt  *time.Time `json:"mergedAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// LandedLister lists recently merged pull/merge requests in a repo, so landed
// changes in profile repos are tracked even without a notification.
type LandedLister interface {
	RecentlyMerged(ctx context.Context, repo string, since time.Time, limit int) ([]ChangeRef, error)
}

// MaxChangeFiles caps how many changed files a Changer returns.
const MaxChangeFiles = 300

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

// Comment is one discussion entry on an issue / pull request / merge request,
// as listed for agent deep-dives (oldest first).
type Comment struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // comment | review | review_comment | note
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	HTMLURL   string    `json:"htmlUrl,omitempty"`
	Path      string    `json:"path,omitempty"`  // review comments / diff notes: the file
	State     string    `json:"state,omitempty"` // reviews: approved | changes_requested | commented
}

// Discusser is implemented by sources that can list an item's discussion.
// Implementations return at most limit entries, keeping the most recent ones.
type Discusser interface {
	Comments(ctx context.Context, repo string, number int, subjectType string, limit int) ([]Comment, error)
}

// MaxCommentBody caps one comment body in Discusser results.
const MaxCommentBody = 4000
