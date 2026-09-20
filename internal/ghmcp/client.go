// Package ghmcp is the app's MCP client for the official GitHub MCP server.
//
// One Client wraps one github-mcp-server subprocess (stdio transport) for one
// account/token. Every tool call passes through a per-write-mode allowlist, so
// even a server started without --read-only can only be asked to run the tools
// this app has explicitly enabled (see AllowedTools).
package ghmcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// WriteMode is the per-account GitHub write policy (PLAN.md §4.9).
type WriteMode string

const (
	// WriteModeReadOnly starts the server with --read-only; no write tool exists.
	WriteModeReadOnly WriteMode = "readonly"
	// WriteModeNotifications additionally allows the two notification-management
	// writes (mark read/done, unsubscribe). Nothing that touches issues or PRs.
	WriteModeNotifications WriteMode = "notifications"
)

// ParseWriteMode validates a stored/user-supplied mode string.
func ParseWriteMode(s string) (WriteMode, error) {
	switch WriteMode(s) {
	case "", WriteModeReadOnly:
		return WriteModeReadOnly, nil
	case WriteModeNotifications:
		return WriteModeNotifications, nil
	}
	return "", fmt.Errorf("ghmcp: unknown write mode %q (want readonly|notifications)", s)
}

// Tool names used by this app, as exposed by github-mcp-server v1.12.x.
const (
	ToolGetMe                          = "get_me"
	ToolListNotifications              = "list_notifications"
	ToolGetNotificationDetails         = "get_notification_details"
	ToolDismissNotification            = "dismiss_notification"
	ToolManageNotificationSubscription = "manage_notification_subscription"
	ToolIssueRead                      = "issue_read"
	ToolPullRequestRead                = "pull_request_read"
	ToolListPullRequests               = "list_pull_requests"
	ToolSearchIssues                   = "search_issues"
	ToolSearchPullRequests             = "search_pull_requests"
)

var readTools = []string{
	ToolGetMe, ToolListNotifications, ToolGetNotificationDetails,
	ToolIssueRead, ToolPullRequestRead, ToolListPullRequests,
	ToolSearchIssues, ToolSearchPullRequests,
}

var writeToolsByMode = map[WriteMode][]string{
	WriteModeNotifications: {ToolDismissNotification, ToolManageNotificationSubscription},
}

// AllowedTools returns the client-side allowlist for a write mode.
func AllowedTools(mode WriteMode) map[string]bool {
	m := make(map[string]bool, len(readTools)+2)
	for _, t := range readTools {
		m[t] = true
	}
	for _, t := range writeToolsByMode[mode] {
		m[t] = true
	}
	return m
}

// DefaultToolsets are the server toolsets the app needs; everything else stays unregistered.
var DefaultToolsets = []string{"context", "notifications", "issues", "pull_requests", "repos"}

// Config describes how to launch one server process.
type Config struct {
	BinaryPath string
	Token      string
	Host       string // GitHub Enterprise hostname; empty for github.com
	WriteMode  WriteMode
	Toolsets   []string  // defaults to DefaultToolsets
	LogFile    string    // optional --log-file for the server
	Stderr     io.Writer // server stderr sink; defaults to io.Discard
}

func (c Config) args() []string {
	ts := c.Toolsets
	if len(ts) == 0 {
		ts = DefaultToolsets
	}
	args := []string{"stdio", "--toolsets", strings.Join(ts, ",")}
	if c.WriteMode == "" || c.WriteMode == WriteModeReadOnly {
		args = append(args, "--read-only")
	}
	if c.LogFile != "" {
		args = append(args, "--log-file", c.LogFile)
	}
	return args
}

// ReadOnly reports whether the server will be started with --read-only.
func (c Config) ReadOnly() bool { return c.WriteMode == "" || c.WriteMode == WriteModeReadOnly }

// Session is the subset of *mcp.ClientSession the client uses (fakeable in tests).
type Session interface {
	ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
	Close() error
}

type session = Session

// Stats are per-client counters surfaced in Diagnostics.
type Stats struct {
	Calls     map[string]int `json:"calls"`
	Errors    map[string]int `json:"errors"`
	Rejected  int            `json:"rejected"` // calls refused by the allowlist
	Restarts  int            `json:"restarts"`
	LastError string         `json:"lastError"`
	StartedAt time.Time      `json:"startedAt"`
}

// Client manages one server process and enforces the tool allowlist.
type Client struct {
	cfg     Config
	allowed map[string]bool

	mu      sync.Mutex
	sess    session
	stats   Stats
	connect func(ctx context.Context) (session, error)
}

// New prepares a client; the process starts on first use (or Start).
func New(cfg Config) *Client {
	c := &Client{
		cfg:     cfg,
		allowed: AllowedTools(cfg.WriteMode),
		stats:   Stats{Calls: map[string]int{}, Errors: map[string]int{}},
	}
	c.connect = c.spawn
	return c
}

// NewForTest builds a client whose sessions come from connect instead of a
// subprocess (in-memory MCP servers in tests).
func NewForTest(cfg Config, connect func(ctx context.Context) (Session, error)) *Client {
	c := New(cfg)
	c.connect = func(ctx context.Context) (session, error) { return connect(ctx) }
	return c
}

// Config returns the launch configuration (token included; do not log it).
func (c *Client) Config() Config { return c.cfg }

// Allowed reports whether a tool passes the client-side allowlist.
func (c *Client) Allowed(tool string) bool { return c.allowed[tool] }

func (c *Client) spawn(ctx context.Context) (session, error) {
	if c.cfg.BinaryPath == "" {
		return nil, errors.New("ghmcp: no server binary path configured")
	}
	if c.cfg.Token == "" {
		return nil, errors.New("ghmcp: no token configured")
	}
	// Deliberately not exec.CommandContext: the server outlives the Start ctx
	// and is stopped by Close.
	cmd := exec.Command(c.cfg.BinaryPath, c.cfg.args()...)
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GITHUB_PERSONAL_ACCESS_TOKEN=") || strings.HasPrefix(kv, "GITHUB_HOST=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GITHUB_PERSONAL_ACCESS_TOKEN="+c.cfg.Token)
	if c.cfg.Host != "" {
		env = append(env, "GITHUB_HOST="+c.cfg.Host)
	}
	cmd.Env = env
	if c.cfg.Stderr != nil {
		cmd.Stderr = c.cfg.Stderr
	} else {
		cmd.Stderr = io.Discard
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "ghinbox", Version: "0.1.0"}, nil)
	sess, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("ghmcp: start %s: %w", c.cfg.BinaryPath, err)
	}
	return sess, nil
}

// Start launches the server if it is not running. Safe to call repeatedly.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startLocked(ctx)
}

func (c *Client) startLocked(ctx context.Context) error {
	if c.sess != nil {
		return nil
	}
	s, err := c.connect(ctx)
	if err != nil {
		c.stats.LastError = err.Error()
		return err
	}
	c.sess = s
	c.stats.StartedAt = time.Now()
	return nil
}

// Close stops the server process. The client can be started again afterwards.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *Client) closeLocked() error {
	if c.sess == nil {
		return nil
	}
	err := c.sess.Close()
	c.sess = nil
	return err
}

// Restart stops and relaunches the server (used after a write-mode change).
func (c *Client) Restart(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.closeLocked()
	c.stats.Restarts++
	return c.startLocked(ctx)
}

// Stats returns a copy of the counters.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Calls = copyMap(c.stats.Calls)
	s.Errors = copyMap(c.stats.Errors)
	return s
}

func copyMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ToolInfo is one entry from the server's tools/list, annotated with our allowlist.
type ToolInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"readOnly"`
	Allowed     bool   `json:"allowed"`
}

// ListTools asks the server which tools it registered (reflects --read-only and toolsets).
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.startLocked(ctx); err != nil {
		return nil, err
	}
	res, err := c.sess.ListTools(ctx, nil)
	if err != nil {
		c.stats.LastError = err.Error()
		return nil, fmt.Errorf("ghmcp: tools/list: %w", err)
	}
	out := make([]ToolInfo, 0, len(res.Tools))
	for _, t := range res.Tools {
		info := ToolInfo{Name: t.Name, Title: t.Title, Description: firstLine(t.Description), Allowed: c.allowed[t.Name]}
		if t.Annotations != nil {
			info.ReadOnly = t.Annotations.ReadOnlyHint
			if info.Title == "" {
				info.Title = t.Annotations.Title
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// NotAllowedError is returned when a tool is outside the write-mode allowlist.
type NotAllowedError struct {
	Tool string
	Mode WriteMode
}

func (e *NotAllowedError) Error() string {
	return fmt.Sprintf("ghmcp: tool %q is not allowed in write mode %q", e.Tool, e.Mode)
}

// ToolError is a tool-level failure reported by the server (IsError result).
type ToolError struct {
	Tool    string
	Message string
}

func (e *ToolError) Error() string { return fmt.Sprintf("ghmcp: %s: %s", e.Tool, e.Message) }

// CallRaw invokes a tool and returns the concatenated text content.
// Transport failures trigger one automatic restart-and-retry.
func (c *Client) CallRaw(ctx context.Context, tool string, args map[string]any) (string, error) {
	if !c.allowed[tool] {
		c.mu.Lock()
		c.stats.Rejected++
		c.mu.Unlock()
		return "", &NotAllowedError{Tool: tool, Mode: c.cfg.WriteMode}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.startLocked(ctx); err != nil {
		return "", err
	}
	c.stats.Calls[tool]++
	res, err := c.sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil && ctx.Err() == nil {
		// Likely a dead process: restart once and retry.
		_ = c.closeLocked()
		c.stats.Restarts++
		if serr := c.startLocked(ctx); serr == nil {
			res, err = c.sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		}
	}
	if err != nil {
		c.stats.Errors[tool]++
		c.stats.LastError = err.Error()
		return "", fmt.Errorf("ghmcp: %s: %w", tool, err)
	}
	text := joinText(res.Content)
	if res.IsError {
		c.stats.Errors[tool]++
		c.stats.LastError = text
		return "", &ToolError{Tool: tool, Message: text}
	}
	return text, nil
}

func joinText(content []mcp.Content) string {
	var b strings.Builder
	for _, item := range content {
		if t, ok := item.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
