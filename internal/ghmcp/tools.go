package ghmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v89/github"
)

// Notification is the app's view of one GitHub notification thread.
type Notification struct {
	ID               string     `json:"id"`
	Unread           bool       `json:"unread"`
	Reason           string     `json:"reason"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	LastReadAt       *time.Time `json:"lastReadAt"`
	URL              string     `json:"url"`
	Title            string     `json:"title"`
	SubjectType      string     `json:"subjectType"` // Issue, PullRequest, Release, CheckSuite, Discussion, Commit, RepositoryVulnerabilityAlert...
	SubjectURL       string     `json:"subjectUrl"`
	LatestCommentURL string     `json:"latestCommentUrl"`
	Repo             string     `json:"repo"` // owner/name
	RepoOwner        string     `json:"repoOwner"`
	RepoName         string     `json:"repoName"`
	RepoPrivate      bool       `json:"repoPrivate"`
}

// SubjectNumber extracts the issue/PR number from the subject API URL, or 0.
func (n Notification) SubjectNumber() int {
	i := strings.LastIndexByte(n.SubjectURL, '/')
	if i < 0 {
		return 0
	}
	v, err := strconv.Atoi(n.SubjectURL[i+1:])
	if err != nil {
		return 0
	}
	return v
}

// ListNotificationsOpts mirrors list_notifications arguments.
type ListNotificationsOpts struct {
	Since   time.Time
	Before  time.Time
	Filter  string // "", "default", "include_read_notifications", "only_participating"
	Owner   string
	Repo    string
	Page    int
	PerPage int
}

func (o ListNotificationsOpts) args() map[string]any {
	args := map[string]any{}
	if !o.Since.IsZero() {
		args["since"] = o.Since.UTC().Format(time.RFC3339)
	}
	if !o.Before.IsZero() {
		args["before"] = o.Before.UTC().Format(time.RFC3339)
	}
	if o.Filter != "" {
		args["filter"] = o.Filter
	}
	if o.Owner != "" && o.Repo != "" {
		args["owner"] = o.Owner
		args["repo"] = o.Repo
	}
	if o.Page > 0 {
		args["page"] = o.Page
	}
	if o.PerPage > 0 {
		args["perPage"] = o.PerPage
	}
	return args
}

// ListNotifications fetches one page of notification threads.
func (c *Client) ListNotifications(ctx context.Context, opts ListNotificationsOpts) ([]Notification, error) {
	text, err := c.CallRaw(ctx, ToolListNotifications, opts.args())
	if err != nil {
		return nil, err
	}
	var raw []*github.Notification
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("ghmcp: decode %s: %w", ToolListNotifications, err)
	}
	out := make([]Notification, 0, len(raw))
	for _, n := range raw {
		out = append(out, convertNotification(n))
	}
	return out, nil
}

// GetNotificationDetails fetches one thread by id.
func (c *Client) GetNotificationDetails(ctx context.Context, id string) (Notification, error) {
	text, err := c.CallRaw(ctx, ToolGetNotificationDetails, map[string]any{"notificationID": id})
	if err != nil {
		return Notification{}, err
	}
	var raw github.Notification
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return Notification{}, fmt.Errorf("ghmcp: decode %s: %w", ToolGetNotificationDetails, err)
	}
	return convertNotification(&raw), nil
}

// DismissNotification marks a thread "read" or "done" on GitHub (write tool).
func (c *Client) DismissNotification(ctx context.Context, threadID, state string) error {
	if state != "read" && state != "done" {
		return fmt.Errorf("ghmcp: dismiss state must be read|done, got %q", state)
	}
	_, err := c.CallRaw(ctx, ToolDismissNotification, map[string]any{"threadID": threadID, "state": state})
	return err
}

// ManageNotificationSubscription ignores/watches/deletes a thread subscription (write tool).
func (c *Client) ManageNotificationSubscription(ctx context.Context, threadID, action string) error {
	switch action {
	case "ignore", "watch", "delete":
	default:
		return fmt.Errorf("ghmcp: subscription action must be ignore|watch|delete, got %q", action)
	}
	_, err := c.CallRaw(ctx, ToolManageNotificationSubscription, map[string]any{"notificationID": threadID, "action": action})
	return err
}

// Me is the authenticated user.
type Me struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

// GetMe returns the login behind the token.
func (c *Client) GetMe(ctx context.Context) (Me, error) {
	text, err := c.CallRaw(ctx, ToolGetMe, map[string]any{})
	if err != nil {
		return Me{}, err
	}
	var me Me
	if err := json.Unmarshal([]byte(text), &me); err != nil {
		return Me{}, fmt.Errorf("ghmcp: decode %s: %w", ToolGetMe, err)
	}
	if me.Login == "" {
		return Me{}, fmt.Errorf("ghmcp: %s returned no login", ToolGetMe)
	}
	return me, nil
}

// Item is an issue or pull request from search results.
type Item struct {
	Repo      string    `json:"repo"` // owner/name
	Number    int       `json:"number"`
	IsPR      bool      `json:"isPr"`
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
}

// SearchResult is one page of search_issues / search_pull_requests.
type SearchResult struct {
	TotalCount int    `json:"totalCount"`
	Incomplete bool   `json:"incomplete"`
	Items      []Item `json:"items"`
}

// searchFields trims search payloads to what Item needs.
var searchFields = []string{
	"number", "title", "state", "html_url", "repository_url", "user", "assignees",
	"labels", "draft", "comments", "created_at", "updated_at", "pull_request",
}

// SearchIssues runs an issues search (the server scopes it to is:issue).
func (c *Client) SearchIssues(ctx context.Context, query string, page, perPage int) (SearchResult, error) {
	return c.search(ctx, ToolSearchIssues, query, page, perPage)
}

// SearchPullRequests runs a PR search (the server scopes it to is:pr).
func (c *Client) SearchPullRequests(ctx context.Context, query string, page, perPage int) (SearchResult, error) {
	return c.search(ctx, ToolSearchPullRequests, query, page, perPage)
}

func (c *Client) search(ctx context.Context, tool, query string, page, perPage int) (SearchResult, error) {
	args := map[string]any{"query": query, "sort": "updated", "order": "desc", "fields": searchFields}
	if page > 0 {
		args["page"] = page
	}
	if perPage > 0 {
		args["perPage"] = perPage
	}
	text, err := c.CallRaw(ctx, tool, args)
	if err != nil {
		return SearchResult{}, err
	}
	var raw struct {
		TotalCount        int             `json:"total_count"`
		IncompleteResults bool            `json:"incomplete_results"`
		Items             []*github.Issue `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return SearchResult{}, fmt.Errorf("ghmcp: decode %s: %w", tool, err)
	}
	res := SearchResult{TotalCount: raw.TotalCount, Incomplete: raw.IncompleteResults, Items: make([]Item, 0, len(raw.Items))}
	for _, is := range raw.Items {
		res.Items = append(res.Items, convertIssue(is))
	}
	return res, nil
}

// IssueRead calls issue_read with a method (get, get_comments, get_sub_issues, get_parent, get_labels).
func (c *Client) IssueRead(ctx context.Context, owner, repo string, number int, method string) (json.RawMessage, error) {
	text, err := c.CallRaw(ctx, ToolIssueRead, map[string]any{"owner": owner, "repo": repo, "issue_number": number, "method": method})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(text), nil
}

// PullRequestRead calls pull_request_read with a method (get, get_diff, get_status, get_files,
// get_commits, get_review_comments, get_reviews, get_comments, get_check_runs).
func (c *Client) PullRequestRead(ctx context.Context, owner, repo string, number int, method string) (json.RawMessage, error) {
	text, err := c.CallRaw(ctx, ToolPullRequestRead, map[string]any{"owner": owner, "repo": repo, "pullNumber": number, "method": method})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(text), nil
}

func convertNotification(n *github.Notification) Notification {
	out := Notification{
		ID:     n.GetID(),
		Unread: n.GetUnread(),
		Reason: n.GetReason(),
		URL:    n.GetURL(),
	}
	if n.UpdatedAt != nil {
		out.UpdatedAt = n.UpdatedAt.Time.UTC()
	}
	if n.LastReadAt != nil {
		t := n.LastReadAt.Time.UTC()
		out.LastReadAt = &t
	}
	if s := n.Subject; s != nil {
		out.Title = s.GetTitle()
		out.SubjectType = s.GetType()
		out.SubjectURL = s.GetURL()
		out.LatestCommentURL = s.GetLatestCommentURL()
	}
	if r := n.Repository; r != nil {
		out.Repo = r.GetFullName()
		out.RepoName = r.GetName()
		out.RepoOwner = r.GetOwner().GetLogin()
		out.RepoPrivate = r.GetPrivate()
		if out.Repo == "" && out.RepoOwner != "" {
			out.Repo = out.RepoOwner + "/" + out.RepoName
		}
	}
	return out
}

func convertIssue(is *github.Issue) Item {
	it := Item{
		Number:   is.GetNumber(),
		IsPR:     is.PullRequestLinks != nil,
		Title:    is.GetTitle(),
		State:    is.GetState(),
		HTMLURL:  is.GetHTMLURL(),
		Author:   is.GetUser().GetLogin(),
		Draft:    is.GetDraft(),
		Comments: is.GetComments(),
		Repo:     repoFromAPIURL(is.GetRepositoryURL()),
	}
	if is.CreatedAt != nil {
		it.CreatedAt = is.CreatedAt.Time.UTC()
	}
	if is.UpdatedAt != nil {
		it.UpdatedAt = is.UpdatedAt.Time.UTC()
	}
	for _, a := range is.Assignees {
		it.Assignees = append(it.Assignees, a.GetLogin())
	}
	for _, l := range is.Labels {
		it.Labels = append(it.Labels, l.GetName())
	}
	if it.Repo == "" {
		it.Repo = repoFromHTMLURL(it.HTMLURL)
	}
	return it
}

// repoFromAPIURL turns https://api.github.com/repos/owner/name into owner/name.
func repoFromAPIURL(u string) string {
	i := strings.Index(u, "/repos/")
	if i < 0 {
		return ""
	}
	rest := strings.Trim(u[i+len("/repos/"):], "/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// repoFromHTMLURL turns https://github.com/owner/name/pull/1 into owner/name.
func repoFromHTMLURL(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return ""
	}
	parts := strings.Split(strings.Trim(u[i+3:], "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[1] + "/" + parts[2]
}
