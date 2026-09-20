// Command ghinbox is the headless companion to the GH Inbox desktop app. It
// shares the app's database, secrets and pipeline, so it can add accounts,
// sync, and inspect the inbox from a terminal (handy on a Pi over SSH).
//
// Flags must precede positional arguments: ghinbox sync --account octocat --full
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"ghinbox/internal/mcpbin"
	"ghinbox/internal/pipeline"
	"ghinbox/internal/secrets"
	"ghinbox/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	var err error
	switch os.Args[1] {
	case "version":
		v, ok := mcpbin.BundledVersion()
		if !ok {
			v = "none"
		}
		fmt.Printf("ghinbox 0.1.0 (M1); bundled github-mcp-server: %s\n", v)
	case "mcp":
		err = cmdMCP(ctx, os.Args[2:])
	case "account":
		err = cmdAccount(ctx, os.Args[2:])
	case "sync":
		err = cmdSync(ctx, os.Args[2:])
	case "inbox":
		err = cmdInbox(ctx, os.Args[2:])
	case "mine":
		err = cmdMine(ctx, os.Args[2:])
	case "read", "done", "snooze", "undone":
		err = cmdAction(ctx, os.Args[1], os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: ghinbox <command> [flags] [args]

  mcp path                                   locate the github-mcp-server binary
  mcp tools [--account L]                    list the server's tools (read-only unless account write mode says otherwise)
  mcp call [--account L] TOOL [JSON]         call a tool through the allowlist and print its text result
  account add --token-file F [--host H]      validate a PAT with get_me and store it in the keyring
  account list
  account remove LOGIN
  account write-mode LOGIN readonly|notifications
  sync [--account L] [--full]                pull notifications (+ "mine" when due)
  inbox [--account L] [--all] [--noise] [--done] [--limit N]
  mine [--account L] [--closed]
  read|done|undone [--account L] THREAD_ID   local triage state (mirrored to GitHub only in write mode 'notifications')
  snooze [--account L] --for 2h THREAD_ID
  version

env: GHINBOX_DB (database path), GHINBOX_MCP_PATH (server binary override), GHINBOX_DEBUG=1 (server stderr + debug logs)
`)
}

// --- shared setup -----------------------------------------------------------

type env struct {
	db   *store.DB
	sec  secrets.Store
	pipe *pipeline.Pipeline
	mcp  mcpbin.Info
}

func openEnv(needMCP bool) (*env, error) {
	path := os.Getenv("GHINBOX_DB")
	if path == "" {
		var err error
		if path, err = store.DefaultPath(); err != nil {
			return nil, err
		}
	}
	db, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", path, err)
	}
	e := &env{db: db, sec: secrets.Open()}
	level := slog.LevelWarn
	var serverLog io.Writer = io.Discard
	if os.Getenv("GHINBOX_DEBUG") != "" {
		level = slog.LevelDebug
		serverLog = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if needMCP {
		info, err := mcpbin.Locate(mcpbin.Options{OverridePath: os.Getenv("GHINBOX_MCP_PATH")})
		if err != nil {
			db.Close()
			return nil, err
		}
		e.mcp = info
	}
	e.pipe = pipeline.New(pipeline.Deps{DB: db, Secrets: e.sec, MCPPath: e.mcp.Path, Logger: logger, ServerLog: serverLog})
	return e, nil
}

func (e *env) close() {
	e.pipe.Close()
	e.db.Close()
}

// account resolves --account; with one configured account the flag is optional.
func (e *env) account(ctx context.Context, login string) (store.Account, error) {
	accts, err := e.db.ListAccounts(ctx)
	if err != nil {
		return store.Account{}, err
	}
	if login == "" {
		switch len(accts) {
		case 0:
			return store.Account{}, errors.New("no accounts configured; run: ghinbox account add --token-file <file>")
		case 1:
			return accts[0], nil
		default:
			return store.Account{}, errors.New("several accounts configured; pass --account LOGIN")
		}
	}
	for _, a := range accts {
		if strings.EqualFold(a.Login, login) {
			return a, nil
		}
	}
	return store.Account{}, fmt.Errorf("unknown account %q", login)
}

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	acct := fs.String("account", "", "account login (optional when only one is configured)")
	return fs, acct
}

// --- mcp --------------------------------------------------------------------

func cmdMCP(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("mcp: want path|tools|call")
	}
	switch args[0] {
	case "path":
		info, err := mcpbin.Locate(mcpbin.Options{OverridePath: os.Getenv("GHINBOX_MCP_PATH")})
		if err != nil {
			return err
		}
		fmt.Printf("path:    %s\nsource:  %s\nversion: %s\n", info.Path, info.Source, orUnknown(info.Version))
		return nil
	case "tools":
		fs, login := newFlags("mcp tools")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		e, err := openEnv(true)
		if err != nil {
			return err
		}
		defer e.close()
		acct, err := e.account(ctx, *login)
		if err != nil {
			return err
		}
		client, err := e.pipe.Client(ctx, acct)
		if err != nil {
			return err
		}
		tools, err := client.ListTools(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintf(w, "TOOL\tREAD-ONLY\tALLOWED\tTITLE\n")
		for _, t := range tools {
			fmt.Fprintf(w, "%s\t%v\t%v\t%s\n", t.Name, t.ReadOnly, t.Allowed, t.Title)
		}
		w.Flush()
		fmt.Printf("\n%d tools; account %s write mode %s (server --read-only=%v)\n", len(tools), acct.Login, acct.WriteMode, client.Config().ReadOnly())
		return nil
	case "call":
		fs, login := newFlags("mcp call")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) < 1 {
			return errors.New("mcp call: want TOOL [JSON]")
		}
		toolArgs := map[string]any{}
		if len(rest) > 1 {
			if err := json.Unmarshal([]byte(rest[1]), &toolArgs); err != nil {
				return fmt.Errorf("mcp call: arguments must be a JSON object: %w", err)
			}
		}
		e, err := openEnv(true)
		if err != nil {
			return err
		}
		defer e.close()
		acct, err := e.account(ctx, *login)
		if err != nil {
			return err
		}
		client, err := e.pipe.Client(ctx, acct)
		if err != nil {
			return err
		}
		text, err := client.CallRaw(ctx, rest[0], toolArgs)
		if err != nil {
			return err
		}
		fmt.Println(text)
		return nil
	}
	return fmt.Errorf("mcp: unknown subcommand %q", args[0])
}

// --- account ----------------------------------------------------------------

func cmdAccount(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("account: want add|list|remove|write-mode")
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("account add", flag.ContinueOnError)
		tokenFile := fs.String("token-file", "", "file containing the personal access token (first line)")
		host := fs.String("host", "", "GitHub Enterprise host (default github.com)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *tokenFile == "" {
			return errors.New("account add: --token-file is required")
		}
		raw, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		token := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
		if token == "" {
			return errors.New("account add: token file is empty")
		}
		e, err := openEnv(true)
		if err != nil {
			return err
		}
		defer e.close()
		acct, err := e.pipe.AddAccount(ctx, token, *host)
		if err != nil {
			return err
		}
		fmt.Printf("account %s@%s (id %d) ready; token stored in %s; write mode %s\n", acct.Login, acct.Host, acct.ID, e.sec.Backend(), acct.WriteMode)
		return nil
	case "list":
		e, err := openEnv(false)
		if err != nil {
			return err
		}
		defer e.close()
		accts, err := e.db.ListAccounts(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintf(w, "ID\tLOGIN\tHOST\tWRITE MODE\tLAST SYNC\tLAST ERROR\n")
		for _, a := range accts {
			st, _ := e.db.GetSyncState(ctx, a.ID)
			last := "never"
			if st.LastSyncAt != nil {
				last = st.LastSyncAt.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", a.ID, a.Login, a.Host, a.WriteMode, last, st.LastError)
		}
		w.Flush()
		fmt.Printf("secrets backend: %s\n", e.sec.Backend())
		return nil
	case "remove":
		if len(args) < 2 {
			return errors.New("account remove: want LOGIN")
		}
		e, err := openEnv(false)
		if err != nil {
			return err
		}
		defer e.close()
		acct, err := e.account(ctx, args[1])
		if err != nil {
			return err
		}
		return e.pipe.RemoveAccount(ctx, acct.ID)
	case "write-mode":
		if len(args) < 3 {
			return errors.New("account write-mode: want LOGIN readonly|notifications")
		}
		e, err := openEnv(false)
		if err != nil {
			return err
		}
		defer e.close()
		acct, err := e.account(ctx, args[1])
		if err != nil {
			return err
		}
		if err := e.pipe.SetWriteMode(ctx, acct.ID, args[2]); err != nil {
			return err
		}
		fmt.Printf("account %s write mode set to %s (server restarts with matching --read-only flag)\n", acct.Login, args[2])
		return nil
	}
	return fmt.Errorf("account: unknown subcommand %q", args[0])
}

// --- sync / inbox / mine ----------------------------------------------------

func cmdSync(ctx context.Context, args []string) error {
	fs, login := newFlags("sync")
	full := fs.Bool("full", false, "re-read the last 7 days including read notifications")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openEnv(true)
	if err != nil {
		return err
	}
	defer e.close()
	var reports []pipeline.SyncReport
	if *login != "" {
		acct, err := e.account(ctx, *login)
		if err != nil {
			return err
		}
		rep, _ := e.pipe.SyncAccount(ctx, acct, *full)
		reports = []pipeline.SyncReport{rep}
	} else {
		reports, err = e.pipe.SyncAll(ctx, *full)
		if err != nil {
			return err
		}
	}
	var failed bool
	for _, r := range reports {
		mode := "incremental"
		if r.Full {
			mode = "full"
		}
		fmt.Printf("%s: %s sync fetched %d (new %d, updated %d, noise %d)", r.Login, mode, r.Fetched, r.New, r.Updated, r.Noise)
		if r.MineSynced {
			fmt.Printf("; mine %d items", r.MineItems)
		}
		fmt.Printf(" in %s", r.Duration.Round(time.Millisecond))
		if r.Error != "" {
			fmt.Printf(" — ERROR: %s", r.Error)
			failed = true
		}
		fmt.Println()
	}
	if len(reports) == 0 {
		return errors.New("no accounts configured")
	}
	if failed {
		return errors.New("one or more syncs failed")
	}
	return nil
}

func cmdInbox(ctx context.Context, args []string) error {
	fs, login := newFlags("inbox")
	all := fs.Bool("all", false, "include read threads")
	noise := fs.Bool("noise", false, "include threads the filter marked as noise")
	done := fs.Bool("done", false, "include done threads")
	limit := fs.Int("limit", 200, "max threads")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openEnv(false)
	if err != nil {
		return err
	}
	defer e.close()
	q := store.ThreadQuery{IncludeRead: *all, IncludeNoise: *noise, IncludeDone: *done, Limit: *limit}
	if *login != "" {
		acct, err := e.account(ctx, *login)
		if err != nil {
			return err
		}
		q.AccountID = acct.ID
	}
	threads, err := e.db.ListThreads(ctx, q)
	if err != nil {
		return err
	}
	groups := map[string][]store.Thread{}
	for _, t := range threads {
		groups[t.ActivityKind] = append(groups[t.ActivityKind], t)
	}
	kinds := make([]string, 0, len(groups))
	for k := range groups {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kindRank(kinds[i]) < kindRank(kinds[j]) })
	for _, k := range kinds {
		fmt.Printf("== %s (%d)\n", k, len(groups[k]))
		for _, t := range groups[k] {
			flags := ""
			if t.FilterVerdict == "noise" {
				flags += " [noise:" + t.FilterReason + "]"
			}
			if t.DoneAt != nil {
				flags += " [done]"
			}
			if t.IsRead() {
				flags += " [read]"
			}
			fmt.Printf("  %-12s %-32s #%-6d %s%s  (%s, %s)\n", t.ThreadID, trunc(t.Repo, 32), t.SubjectNumber, trunc(t.Title, 70), flags, t.Reason, t.UpdatedAt.Local().Format("Jan 02 15:04"))
		}
	}
	c, err := e.db.Counts(ctx, q.AccountID)
	if err != nil {
		return err
	}
	fmt.Printf("\n%d shown; unread %d, noise %d, done %d, mine %d\n", len(threads), c.Unread, c.Noise, c.Done, c.Items)
	return nil
}

func kindRank(k string) int {
	order := []string{"review_requested", "mention", "assignment", "new_pr", "new_issue", "review", "comment", "state_change", "ci", "security", "release", "discussion", "commit", "other"}
	for i, o := range order {
		if o == k {
			return i
		}
	}
	return len(order)
}

func cmdMine(ctx context.Context, args []string) error {
	fs, login := newFlags("mine")
	closed := fs.Bool("closed", false, "include closed items")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openEnv(false)
	if err != nil {
		return err
	}
	defer e.close()
	var acctID int64
	if *login != "" {
		acct, err := e.account(ctx, *login)
		if err != nil {
			return err
		}
		acctID = acct.ID
	}
	items, err := e.db.ListItems(ctx, acctID, *closed)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "KIND\tREPO\t#\tSTATE\tRELATIONS\tTITLE\tUPDATED\n")
	for _, it := range items {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", it.Kind, it.Repo, it.Number, it.State, strings.Join(it.Relations, ","), trunc(it.Title, 60), it.UpdatedAt.Local().Format("Jan 02"))
	}
	w.Flush()
	fmt.Printf("%d items\n", len(items))
	return nil
}

func cmdAction(ctx context.Context, action string, args []string) error {
	fs, login := newFlags(action)
	dur := fs.Duration("for", 24*time.Hour, "snooze duration")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("%s: want THREAD_ID", action)
	}
	e, err := openEnv(action == "read" || action == "done")
	if err != nil {
		return err
	}
	defer e.close()
	acct, err := e.account(ctx, *login)
	if err != nil {
		return err
	}
	id := fs.Arg(0)
	var res pipeline.MirrorResult
	switch action {
	case "read":
		res, err = e.pipe.MarkRead(ctx, acct, id)
	case "done":
		res, err = e.pipe.MarkDone(ctx, acct, id)
	case "undone":
		err = e.pipe.UndoDone(ctx, acct, id)
	case "snooze":
		err = e.pipe.Snooze(ctx, acct, id, time.Now().Add(*dur))
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s %s: ok (GitHub mirrored: %v)", action, id, res.Mirrored)
	if res.Warning != "" {
		fmt.Printf(" — %s", res.Warning)
	}
	fmt.Println()
	return nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
