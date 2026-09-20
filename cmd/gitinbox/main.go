// Command gitinbox is the headless companion to the GitInbox desktop app. It
// shares the app's database, secrets and pipeline, so it can add accounts,
// sync, and inspect the inbox from a terminal (handy on a Pi over SSH).
//
// Flags must precede positional arguments: gitinbox sync --account octocat --full
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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"gitinbox/internal/mcpbin"
	"gitinbox/internal/pipeline"
	"gitinbox/internal/secrets"
	gitlabsrc "gitinbox/internal/source/gitlab"
	"gitinbox/internal/store"
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
		fmt.Printf("gitinbox 0.1.0 (M1); bundled github-mcp-server: %s\n", v)
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
	case "watch":
		err = cmdWatch(ctx, os.Args[2:])
	case "judge":
		err = cmdJudge(ctx, os.Args[2:])
	case "explain":
		err = cmdExplain(ctx, os.Args[2:])
	case "settings":
		err = cmdSettings(ctx, os.Args[2:])
	case "jev-key":
		err = cmdJevKey(ctx, os.Args[2:])
	case "analyze":
		err = cmdAnalyze(ctx, os.Args[2:])
	case "impact":
		err = cmdImpact(ctx, os.Args[2:])
	case "profiles":
		err = cmdProfiles(ctx, os.Args[2:])
	case "summarize":
		err = cmdSummarize(ctx, os.Args[2:])
	case "digest":
		err = cmdDigest(ctx, os.Args[2:])
	case "usage":
		err = cmdUsage(ctx)
	case "eval":
		err = cmdEval(ctx, os.Args[2:])
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
	fmt.Fprint(os.Stderr, `usage: gitinbox <command> [flags] [args]

  mcp path                                   locate the github-mcp-server binary
  mcp tools [--account L]                    list the server's tools (read-only unless account write mode says otherwise)
  mcp call [--account L] TOOL [JSON]         call a tool through the allowlist and print its text result
  account add --token-file F [--forge github|gitlab] [--host H]
                                             validate a PAT (get_me / GET /user) and store it in the keyring
  account list
  account remove LOGIN
  account write-mode LOGIN readonly|notifications
  sync [--account L] [--full]                pull notifications (+ "mine" when due)
  inbox [--account L] [--all] [--noise] [--done] [--limit N]
  mine [--account L] [--closed] [--refresh]
  watch add|remove [--account L] PATH        (GitLab) poll a project's activity, e.g. dev/core
  watch list [--account L]
  judge [--account L] [--limit N]            enrich + judge stale/unjudged unread threads (triage.v1)
  explain [--account L] [--now] THREAD_ID    show state, answers, probabilities and score (--now judges first)
  settings [show | set KEY VALUE]            KEY: provider(auto|jev|ollama|off) jev.model jev.url ollama.url ollama.model max concurrency interests
                                             impact.auto impact.max impact.note-model impact.note-level(2|3|4) impact.scan-landed impact.landed-days
                                             summary.model summary.top summary.every digest.model digest.auto digest.hours notify.needs-me notify.impact-level(3|4)
  jev-key --token-file F                     store the TypeSafe Jev API key in the keyring (empty file removes it)
  analyze [--account L] [--profile ID] [--force] [--note] REPO#N   impact-analyse one PR/MR (auto profile, generic fallback)
  analyze [--account L] --pending            analyse pending PR threads in profile repos + scan landed changes
  impact [--landed] [--min 0-3] [--account L]  list analysed PRs (open by default; --landed for merged)
  profiles list | show ID | enable ID | disable ID | import FILE | delete ID
  summarize [--account L] [--force] THREAD_ID   generate/show the Ollama summary of a thread
  summarize --top N                          summarise the N highest-priority threads lacking a summary
  digest [--hours 24] [--generate]           show the latest digest, or generate one for the period
  usage                                      token usage and latency per provider/model
  eval queue [--account L] [--limit N]       threads to label (unlabeled, judged first)
  eval label [--account L] THREAD_ID KEY=VALUE…   keys: category requires_action urgency relevance priority resolved noise note
  eval pr-label [--account L] REPO#N impact=0-3 [kind=…]
  eval judge --provider jev|ollama [--model M] [--force]   re-judge the labeled set for comparison
  eval report [--out DIR]                    metrics per provider (+ writes Markdown; default eval/ in the repo)
  eval tune [--iters N] [--apply]            search weights that maximise NDCG@25 on your labels
  eval export FILE | eval import FILE        labels as JSON lines
  read|done|undone [--account L] THREAD_ID   local triage state (mirrored to GitHub only in write mode 'notifications')
  snooze [--account L] --for 2h THREAD_ID
  version

env: GITINBOX_DB (database path), GITINBOX_MCP_PATH (server binary override), GITINBOX_DEBUG=1 (server stderr + debug logs)
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
	path := os.Getenv("GITINBOX_DB")
	if path == "" {
		var err error
		if path, err = store.DefaultPath(); err != nil {
			return nil, err
		}
	}
	if moved, err := store.MigrateLegacyDataDir(path); err != nil {
		return nil, fmt.Errorf("migrate legacy data dir: %w", err)
	} else if moved {
		fmt.Fprintf(os.Stderr, "migrated data directory from ghinbox to %s\n", filepath.Dir(path))
	}
	db, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", path, err)
	}
	e := &env{db: db, sec: secrets.Open()}
	level := slog.LevelWarn
	var serverLog io.Writer = io.Discard
	if os.Getenv("GITINBOX_DEBUG") != "" {
		level = slog.LevelDebug
		serverLog = os.Stderr
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if needMCP {
		info, err := mcpbin.Locate(mcpbin.Options{OverridePath: os.Getenv("GITINBOX_MCP_PATH")})
		if err != nil {
			db.Close()
			return nil, err
		}
		e.mcp = info
	}
	e.pipe = pipeline.New(pipeline.Deps{DB: db, Secrets: e.sec, MCPPath: e.mcp.Path, Logger: logger, ServerLog: serverLog,
		Notify: func(n pipeline.Notification) {
			fmt.Fprintf(os.Stderr, "NOTIFY: %s — %s %s\n", n.Title, n.Body, n.URL)
		}})
	e.pipe.SetGitLabFactory(gitlabsrc.Factory())
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
			return store.Account{}, errors.New("no accounts configured; run: gitinbox account add --token-file <file>")
		case 1:
			return accts[0], nil
		default:
			return store.Account{}, errors.New("several accounts configured; pass --account LOGIN")
		}
	}
	// Accept an id, "login@host", a host, or a login (which must be unambiguous).
	var matches []store.Account
	for _, a := range accts {
		switch {
		case strconv.FormatInt(a.ID, 10) == login,
			strings.EqualFold(a.Login+"@"+a.Host, login),
			strings.EqualFold(a.Host, login),
			strings.EqualFold(a.Login, login):
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 0:
		return store.Account{}, fmt.Errorf("unknown account %q (use LOGIN@HOST or the id from `gitinbox account list`)", login)
	case 1:
		return matches[0], nil
	}
	var names []string
	for _, a := range matches {
		names = append(names, fmt.Sprintf("%s@%s (id %d)", a.Login, a.Host, a.ID))
	}
	return store.Account{}, fmt.Errorf("account %q is ambiguous: %s — use LOGIN@HOST or the id", login, strings.Join(names, ", "))
}

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	acct := fs.String("account", "", "account: id, LOGIN@HOST, host, or login (optional when only one is configured)")
	return fs, acct
}

// --- mcp --------------------------------------------------------------------

func cmdMCP(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("mcp: want path|tools|call")
	}
	switch args[0] {
	case "path":
		info, err := mcpbin.Locate(mcpbin.Options{OverridePath: os.Getenv("GITINBOX_MCP_PATH")})
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
		client, err := e.pipe.MCPClient(ctx, acct)
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
		client, err := e.pipe.MCPClient(ctx, acct)
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
		forge := fs.String("forge", "github", "github or gitlab")
		host := fs.String("host", "", "forge host (default github.com / gitlab.com)")
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
		acct, err := e.pipe.AddAccount(ctx, *forge, token, *host)
		if err != nil {
			return err
		}
		fmt.Printf("account %s@%s (%s, id %d) ready; token stored in %s; write mode %s; token scopes %s\n", acct.Login, acct.Host, acct.Forge, acct.ID, e.sec.Backend(), acct.WriteMode, orUnknown(acct.TokenScopes))
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
		fmt.Fprintf(w, "ID\tFORGE\tLOGIN\tHOST\tWRITE MODE\tSCOPES\tLAST SYNC\tLAST ERROR\n")
		for _, a := range accts {
			st, _ := e.db.GetSyncState(ctx, a.ID)
			last := "never"
			if st.LastSyncAt != nil {
				last = st.LastSyncAt.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", a.ID, a.Forge, a.Login, a.Host, a.WriteMode, orUnknown(a.TokenScopes), last, st.LastError)
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
		for _, w := range r.Warnings {
			fmt.Printf(" — warning: %s", w)
		}
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
	scored, err := e.pipe.ScoreThreads(ctx, threads)
	if err != nil {
		return err
	}
	groups := map[string][]pipeline.Scored{}
	for _, sc := range scored {
		k := sc.Thread.ActivityKind
		if sc.Score.Pinned || sc.Score.Bucket == "needs_me" {
			k = "needs_me"
		} else if sc.Score.Bucket == "resolved" {
			k = "resolved"
		}
		groups[k] = append(groups[k], sc)
	}
	kinds := make([]string, 0, len(groups))
	for k := range groups {
		kinds = append(kinds, k)
		sort.SliceStable(groups[k], func(i, j int) bool { return groups[k][i].Score.Priority > groups[k][j].Score.Priority })
	}
	sort.Slice(kinds, func(i, j int) bool { return kindRank(kinds[i]) < kindRank(kinds[j]) })
	for _, k := range kinds {
		fmt.Printf("== %s (%d)\n", k, len(groups[k]))
		for _, sc := range groups[k] {
			t := sc.Thread
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
			cat := ""
			if sc.Score.Judged {
				cat = " " + sc.Score.Category
				if sc.Score.Unsure {
					cat += "?"
				}
			}
			fmt.Printf("  %3d%%%-22s %-12s %-28s #%-6d %s%s  (%s, %s)\n", sc.Score.Percent, cat, t.ThreadID, trunc(t.Repo, 28), t.SubjectNumber, trunc(t.Title, 60), flags, t.Reason, t.UpdatedAt.Local().Format("Jan 02 15:04"))
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
	order := []string{"needs_me", "review_requested", "mention", "assignment", "new_pr", "new_issue", "review", "comment", "state_change", "ci", "security", "release", "discussion", "commit", "other", "resolved"}
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
	refresh := fs.Bool("refresh", false, "re-run the searches now (otherwise sync refreshes them every 10 minutes)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openEnv(*refresh)
	if err != nil {
		return err
	}
	defer e.close()
	var acctID int64
	if *login != "" || *refresh {
		acct, err := e.account(ctx, *login)
		if err != nil {
			return err
		}
		acctID = acct.ID
		if *refresh {
			n, err := e.pipe.SyncMine(ctx, acct)
			if err != nil {
				return err
			}
			fmt.Printf("refreshed: %d items for %s@%s\n", n, acct.Login, acct.Host)
		}
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

func cmdWatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("watch: want add|remove|list")
	}
	fs, login := newFlags("watch " + args[0])
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	e, err := openEnv(false)
	if err != nil {
		return err
	}
	defer e.close()
	acct, err := e.account(ctx, *login)
	if err != nil {
		return err
	}
	if acct.Forge != store.ForgeGitLab {
		return fmt.Errorf("watch: account %s is %s; watched projects only apply to GitLab accounts", acct.Login, acct.Forge)
	}
	switch args[0] {
	case "add", "remove":
		if fs.NArg() < 1 {
			return fmt.Errorf("watch %s: want PATH (e.g. dev/core)", args[0])
		}
		if args[0] == "add" {
			if err := e.db.AddWatched(ctx, acct.ID, fs.Arg(0)); err != nil {
				return err
			}
			fmt.Printf("watching %s on %s (resolved on next sync)\n", fs.Arg(0), acct.Host)
			return nil
		}
		return e.db.RemoveWatched(ctx, acct.ID, fs.Arg(0))
	case "list":
		ws, err := e.db.ListWatched(ctx, acct.ID)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintf(w, "PATH\tPROJECT ID\tLAST EVENT\tLAST ERROR\n")
		for _, x := range ws {
			last := "never"
			if x.LastEventAt != nil {
				last = x.LastEventAt.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", x.Path, x.ProjectID, last, x.LastError)
		}
		w.Flush()
		return nil
	}
	return fmt.Errorf("watch: unknown subcommand %q", args[0])
}

func cmdJudge(ctx context.Context, args []string) error {
	fs, login := newFlags("judge")
	limit := fs.Int("limit", 0, "max threads this run (0 = settings max)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openEnv(true)
	if err != nil {
		return err
	}
	defer e.close()
	var reports []pipeline.JudgeReport
	if *login != "" {
		acct, err := e.account(ctx, *login)
		if err != nil {
			return err
		}
		rep, jerr := e.pipe.JudgeAccount(ctx, acct, *limit)
		if jerr != nil && rep.Error == "" {
			rep.Error = jerr.Error()
		}
		reports = []pipeline.JudgeReport{rep}
	} else {
		reports, err = e.pipe.JudgeAll(ctx)
		if err != nil {
			return err
		}
	}
	var failed bool
	for _, r := range reports {
		fmt.Printf("%s: provider %s %s — candidates %d, judged %d, enriched %d, failed %d, tokens in/out %d/%d, %s\n",
			r.Login, orUnknown(r.Provider), r.Model, r.Candidates, r.Judged, r.Enriched, r.Failed, r.Usage.InputTokens, r.Usage.OutputTokens, r.Duration.Round(time.Millisecond))
		for _, m := range r.Errors {
			fmt.Printf("  - %s\n", m)
		}
		if r.Error != "" {
			fmt.Printf("  ERROR: %s\n", r.Error)
			failed = true
		}
	}
	if failed {
		return errors.New("judge run reported errors")
	}
	return nil
}

func cmdExplain(ctx context.Context, args []string) error {
	fs, login := newFlags("explain")
	now := fs.Bool("now", false, "judge the thread now before explaining")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("explain: want THREAD_ID")
	}
	e, err := openEnv(*now)
	if err != nil {
		return err
	}
	defer e.close()
	acct, err := e.account(ctx, *login)
	if err != nil {
		return err
	}
	ex, err := e.pipe.Explain(ctx, acct, fs.Arg(0), *now)
	if err != nil {
		return err
	}
	t := ex.Thread
	fmt.Printf("%s %s#%d — %s\n  kind %s, reason %s, actor %s, version %s\n", t.Repo, t.SubjectType, t.SubjectNumber, t.Title, t.ActivityKind, t.Reason, orUnknown(t.Actor), ex.Version)
	if t.EnrichedVersion != "" {
		fmt.Printf("  item: author %s, state %s, labels %v, body %d chars; latest: %s %q\n", orUnknown(t.ItemAuthor), orUnknown(t.ItemState), t.ItemLabels, len(t.ItemBody), orUnknown(t.LatestAuthor), trunc(t.LatestBody, 80))
	} else {
		fmt.Println("  (not enriched)")
	}
	if ex.Judgment == nil {
		fmt.Println("  no judgment stored (run: gitinbox judge, or explain --now)")
	} else {
		stale := ""
		if ex.Stale {
			stale = " (STALE: thread changed since)"
		}
		fmt.Printf("  judged by %s %s at %s%s\n", ex.Judgment.Provider, ex.Judgment.Model, ex.Judgment.CreatedAt.Local().Format("Jan 02 15:04"), stale)
		for _, q := range ex.Questions {
			a, ok := ex.Answers[q.ID]
			if !ok {
				continue
			}
			switch a.Kind {
			case "noul":
				fmt.Printf("    %-24s %.2f\n", q.ID, a.Noul)
			case "choice":
				fmt.Printf("    %-24s %s (conf %.2f)  %s\n", q.ID, a.Choice, a.Confidence, probs(a.Probabilities))
			case "score":
				fmt.Printf("    %-24s %.2f/%d (conf %.2f)  %s\n", q.ID, a.Score, len(a.Legend)-1, a.Confidence, probs(a.Probabilities))
			}
		}
	}
	sc := ex.Score
	fmt.Printf("  score: %d%% (priority %.2f) bucket %s pinned %v unsure %v category %s next %s\n", sc.Percent, sc.Priority, sc.Bucket, sc.Pinned, sc.Unsure, orUnknown(sc.Category), orUnknown(sc.NextAction))
	return nil
}

func probs(m map[string]float64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.2f", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func cmdSettings(ctx context.Context, args []string) error {
	e, err := openEnv(false)
	if err != nil {
		return err
	}
	defer e.close()
	st, err := e.pipe.JudgeSettings(ctx)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "show" {
		fmt.Printf("provider:      %s\njev.model:     %s\njev.url:       %s\njev key:       %v\nollama.url:    %s\nollama.model:  %s\nmax:           %d\nconcurrency:   %d\ninterests:     %s\n",
			st.Provider, st.JevModel, st.JevBaseURL, e.pipe.HasJevKey(), st.OllamaURL, orUnknown(st.OllamaJudgeModel), st.MaxPerRun, st.Concurrency, orUnknown(st.ProfileInterests))
		is, _ := e.pipe.ImpactSettings(ctx)
		fmt.Printf("impact.auto:   %v\nimpact.max:    %d\nimpact.note-model: %s\nimpact.note-level: %d\nimpact.scan-landed: %v (every %d min, look back %d days)\n",
			is.AutoAnalyze, is.MaxPerRun, orUnknown(is.NoteModel), is.AutoNoteMinLevel, is.ScanLanded, is.ScanLandedEveryMin, is.LandedLookbackDays)
		ps, _ := e.pipe.ProseSettings(ctx)
		fmt.Printf("summary.model: %s\nsummary.top:   %d (every %d min)\ndigest.model:  %s\ndigest.auto:   %v (every %d h, max %d threads)\nnotify.needs-me: %v\nnotify.impact-level: %d\n",
			orUnknown(ps.SummaryModel), ps.TopN, ps.SummarizeEveryMin, orUnknown(ps.DigestModel), ps.AutoDigest, ps.DigestEveryH, ps.DigestMaxThreads, ps.NotifyNeedsMe, ps.NotifyImpactMinLevel)
		return nil
	}
	if args[0] != "set" || len(args) < 3 {
		return errors.New("settings: want show | set KEY VALUE")
	}
	key, val := args[1], strings.Join(args[2:], " ")
	switch key {
	case "provider":
		switch val {
		case "auto", "jev", "ollama", "off":
			st.Provider = val
		default:
			return errors.New("provider must be auto|jev|ollama|off")
		}
	case "jev.model":
		st.JevModel = val
	case "jev.url":
		st.JevBaseURL = val
	case "ollama.url":
		st.OllamaURL = val
	case "ollama.model":
		st.OllamaJudgeModel = val
	case "interests":
		st.ProfileInterests = val
	case "max":
		fmt.Sscanf(val, "%d", &st.MaxPerRun)
	case "concurrency":
		fmt.Sscanf(val, "%d", &st.Concurrency)
	case "summary.model", "summary.top", "summary.every", "digest.model", "digest.auto", "digest.hours", "notify.needs-me", "notify.impact-level":
		ps, err := e.pipe.ProseSettings(ctx)
		if err != nil {
			return err
		}
		onoff := val == "true" || val == "on" || val == "1"
		switch key {
		case "summary.model":
			ps.SummaryModel = val
		case "summary.top":
			fmt.Sscanf(val, "%d", &ps.TopN)
		case "summary.every":
			fmt.Sscanf(val, "%d", &ps.SummarizeEveryMin)
		case "digest.model":
			ps.DigestModel = val
		case "digest.auto":
			ps.AutoDigest = onoff
		case "digest.hours":
			fmt.Sscanf(val, "%d", &ps.DigestEveryH)
		case "notify.needs-me":
			ps.NotifyNeedsMe = onoff
		case "notify.impact-level":
			fmt.Sscanf(val, "%d", &ps.NotifyImpactMinLevel)
		}
		if err := e.pipe.SetProseSettings(ctx, ps); err != nil {
			return err
		}
		fmt.Printf("%s = %s\n", key, val)
		return nil
	case "impact.max", "impact.note-model", "impact.auto", "impact.scan-landed", "impact.note-level", "impact.landed-days":
		is, err := e.pipe.ImpactSettings(ctx)
		if err != nil {
			return err
		}
		switch key {
		case "impact.max":
			fmt.Sscanf(val, "%d", &is.MaxPerRun)
		case "impact.note-model":
			is.NoteModel = val
		case "impact.auto":
			is.AutoAnalyze = val == "true" || val == "on" || val == "1"
		case "impact.scan-landed":
			is.ScanLanded = val == "true" || val == "on" || val == "1"
		case "impact.note-level":
			fmt.Sscanf(val, "%d", &is.AutoNoteMinLevel)
		case "impact.landed-days":
			fmt.Sscanf(val, "%d", &is.LandedLookbackDays)
		}
		if err := e.pipe.SetImpactSettings(ctx, is); err != nil {
			return err
		}
		fmt.Printf("%s = %s\n", key, val)
		return nil
	default:
		return fmt.Errorf("settings: unknown key %q", key)
	}
	if err := e.pipe.SetJudgeSettings(ctx, st); err != nil {
		return err
	}
	fmt.Printf("%s = %s\n", key, val)
	return nil
}

func cmdJevKey(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("jev-key", flag.ContinueOnError)
	file := fs.String("token-file", "", "file containing the Jev API key (first line); empty file removes the key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("jev-key: --token-file is required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	e, err := openEnv(false)
	if err != nil {
		return err
	}
	defer e.close()
	key := strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	if err := e.pipe.SetJevKey(key); err != nil {
		return err
	}
	if key == "" {
		fmt.Println("Jev key removed")
	} else {
		fmt.Printf("Jev key stored in %s\n", e.sec.Backend())
	}
	return nil
}

func cmdAnalyze(ctx context.Context, args []string) error {
	fs, login := newFlags("analyze")
	profile := fs.String("profile", "", "profile id (default: matching profile, else generic)")
	force := fs.Bool("force", false, "re-run even if the head sha is unchanged")
	note := fs.Bool("note", false, "also generate the impact note (Ollama)")
	pending := fs.Bool("pending", false, "analyse pending PR threads in profile repos and scan landed changes")
	if err := fs.Parse(args); err != nil {
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
	if *pending {
		rep, err := e.pipe.AnalyzePending(ctx, acct, 0)
		fmt.Printf("%s: candidates %d, analysed %d, skipped %d, failed %d, landed %d in %s\n", rep.Login, rep.Candidates, rep.Analysed, rep.Skipped, rep.Failed, rep.Landed, rep.Duration.Round(time.Millisecond))
		for _, m := range rep.Errors {
			fmt.Printf("  - %s\n", m)
		}
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("analyze: want REPO#N (e.g. civicrm/civicrm-core#36990) or --pending")
	}
	repo, number, err := parseRef(fs.Arg(0))
	if err != nil {
		return err
	}
	a, ran, err := e.pipe.AnalyzePR(ctx, acct, repo, number, *profile, *force)
	if err != nil {
		return err
	}
	printAnalysis(a, ran)
	if *note {
		n, err := e.pipe.ImpactNote(ctx, acct, repo, number)
		if err != nil {
			return err
		}
		fmt.Printf("\nNOTE (%s):\n  what changed: %s\n  why it matters: %s\n  surfaces: %s\n  checks: %s\n  migration: %s\n  confidence: %s\n",
			"ollama", n.WhatChanged, n.WhyItMattersForDownstream, strings.Join(n.SurfacesChanged, "; "), strings.Join(n.RecommendedChecks, "; "), n.MigrationHints, n.ConfidenceNote)
	}
	return nil
}

var impactLevels = []string{"none", "possible", "likely", "certain"}

func levelName(l int) string {
	if l < 0 || l >= len(impactLevels) {
		return "unanalysed"
	}
	return impactLevels[l]
}

func printAnalysis(a store.Analysis, ran bool) {
	via := "reused (same head sha)"
	if ran {
		via = "analysed now"
	}
	errText := ""
	if a.Error != "" {
		errText = " ERROR: " + a.Error
	}
	fmt.Printf("%s#%d — %s [%s] %s\n  profile %s, head %s, judged by %s %s%s\n", a.Repo, a.Number, a.Title, a.State, via, a.ProfileID, trunc(a.HeadSHA, 12), orUnknown(a.Provider), a.Model, errText)
	var report struct {
		Layers []struct {
			ID        string   `json:"id"`
			Files     []string `json:"files"`
			Additions int      `json:"additions"`
			Deletions int      `json:"deletions"`
		} `json:"layers"`
		Signals     []string `json:"signals"`
		SurfaceHits []struct {
			PatternID string `json:"patternId"`
			Path      string `json:"path"`
			Line      string `json:"line"`
		} `json:"surfaceHits"`
		TotalFiles int `json:"totalFiles"`
		Ignored    int `json:"ignored"`
	}
	_ = json.Unmarshal(a.ReportJSON, &report)
	fmt.Printf("  files %d (ignored %d); signals %v\n", report.TotalFiles, report.Ignored, report.Signals)
	for _, l := range report.Layers {
		fmt.Printf("  layer %-12s %3d files +%d/-%d  e.g. %s\n", l.ID, len(l.Files), l.Additions, l.Deletions, trunc(strings.Join(l.Files, ", "), 80))
	}
	for i, h := range report.SurfaceHits {
		if i >= 6 {
			fmt.Printf("  … %d more surface hits\n", len(report.SurfaceHits)-6)
			break
		}
		fmt.Printf("  surface %-18s %s: %s\n", h.PatternID, h.Path, trunc(h.Line, 90))
	}
	var answers map[string]struct {
		Kind          string             `json:"kind"`
		Noul          float64            `json:"noul"`
		Choice        string             `json:"choice"`
		Score         float64            `json:"score"`
		Confidence    float64            `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	}
	_ = json.Unmarshal(a.AnswersJSON, &answers)
	for _, id := range []string{"change_kind", "downstream_impact", "affects_downstream", "backward_compatible", "needs_my_attention_now"} {
		ans, ok := answers[id]
		if !ok {
			continue
		}
		switch ans.Kind {
		case "noul":
			fmt.Printf("    %-24s %.2f\n", id, ans.Noul)
		case "choice":
			fmt.Printf("    %-24s %s (conf %.2f)  %s\n", id, ans.Choice, ans.Confidence, probs(ans.Probabilities))
		case "score":
			fmt.Printf("    %-24s %.2f/3 → %s (conf %.2f)  %s\n", id, ans.Score, levelName(a.ImpactLevel), ans.Confidence, probs(ans.Probabilities))
		}
	}
}

func parseRef(s string) (string, int, error) {
	i := strings.LastIndexAny(s, "#!")
	if i <= 0 {
		return "", 0, fmt.Errorf("want REPO#N, got %q", s)
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return "", 0, fmt.Errorf("want REPO#N, got %q", s)
	}
	return s[:i], n, nil
}

func cmdImpact(ctx context.Context, args []string) error {
	fs, login := newFlags("impact")
	landed := fs.Bool("landed", false, "merged PRs only (default: open/closed)")
	min := fs.Int("min", 0, "minimum impact level 0-3")
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
	views, err := e.pipe.ListImpact(ctx, store.AnalysisQuery{AccountID: acctID, MinLevel: *min, Landed: *landed, Limit: 200})
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "LEVEL\tKIND\tREPO\t#\tSTATE\tLAYERS\tHITS\tNOTE\tTITLE\n")
	for _, v := range views {
		var layers []string
		for _, l := range v.Report.Layers {
			layers = append(layers, l.ID)
		}
		note := ""
		if v.Note != nil {
			note = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%d\t%s\t%s\n", levelName(v.Analysis.ImpactLevel), orUnknown(v.Analysis.ChangeKind), v.Analysis.Repo, v.Analysis.Number, v.Analysis.State, strings.Join(layers, ","), len(v.Report.SurfaceHits), note, trunc(v.Analysis.Title, 60))
	}
	w.Flush()
	fmt.Printf("%d analyses\n", len(views))
	return nil
}

func cmdProfiles(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	e, err := openEnv(false)
	if err != nil {
		return err
	}
	defer e.close()
	switch args[0] {
	case "list":
		ps, problems, err := e.pipe.Profiles(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintf(w, "ID\tNAME\tSOURCE\tENABLED\tREPOS\tLAYERS\n")
		for _, p := range ps {
			fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%d\t%d\n", p.ID, p.Name, p.Source, p.Enabled, len(p.Repos), len(p.Layers))
		}
		w.Flush()
		for _, pr := range problems {
			fmt.Println("problem:", pr)
		}
		return nil
	case "show":
		if len(args) < 2 {
			return errors.New("profiles show: want ID")
		}
		ps, _, err := e.pipe.Profiles(ctx)
		if err != nil {
			return err
		}
		for _, p := range ps {
			if p.ID == args[1] {
				fmt.Print(p.YAML)
				return nil
			}
		}
		return fmt.Errorf("profile %q not found", args[1])
	case "enable", "disable":
		if len(args) < 2 {
			return fmt.Errorf("profiles %s: want ID", args[0])
		}
		return e.db.SetProfileEnabled(ctx, args[1], args[0] == "enable")
	case "import":
		if len(args) < 2 {
			return errors.New("profiles import: want FILE")
		}
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		p, err := e.pipe.SaveProfile(ctx, string(raw), true)
		if err != nil {
			return err
		}
		fmt.Printf("profile %s (%s) saved: %d repos, %d layers\n", p.ID, p.Name, len(p.Repos), len(p.Layers))
		return nil
	case "delete":
		if len(args) < 2 {
			return errors.New("profiles delete: want ID")
		}
		return e.db.DeleteUserProfile(ctx, args[1])
	}
	return fmt.Errorf("profiles: unknown subcommand %q", args[0])
}

func cmdSummarize(ctx context.Context, args []string) error {
	fs, login := newFlags("summarize")
	force := fs.Bool("force", false, "regenerate even if a current summary exists")
	top := fs.Int("top", 0, "summarise the N highest-priority threads lacking a summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openEnv(true)
	if err != nil {
		return err
	}
	defer e.close()
	if *top > 0 {
		n, err := e.pipe.SummarizeTop(ctx, *top)
		fmt.Printf("summarised %d threads\n", n)
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("summarize: want THREAD_ID or --top N")
	}
	acct, err := e.account(ctx, *login)
	if err != nil {
		return err
	}
	start := time.Now()
	v, err := e.pipe.Summarize(ctx, acct, fs.Arg(0), *force)
	if err != nil {
		return err
	}
	fmt.Printf("%s (%s, %s, %s)\n  %s\n", fs.Arg(0), v.Summary.Model, time.Since(start).Round(time.Millisecond), map[bool]string{true: "stale", false: "current"}[v.Stale], v.Content.Summary)
	for _, k := range v.Content.KeyPoints {
		fmt.Printf("  - %s\n", k)
	}
	if len(v.Content.AsksOfMe) > 0 {
		fmt.Printf("  asks of me: %s\n", strings.Join(v.Content.AsksOfMe, "; "))
	}
	if v.Content.ChangedSinceLastRead != "" {
		fmt.Printf("  changed since last read: %s\n", v.Content.ChangedSinceLastRead)
	}
	return nil
}

func cmdDigest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("digest", flag.ContinueOnError)
	hours := fs.Int("hours", 24, "period to cover when generating")
	generate := fs.Bool("generate", false, "generate a new digest now")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := openEnv(false)
	if err != nil {
		return err
	}
	defer e.close()
	var v pipeline.DigestView
	if *generate {
		start := time.Now()
		v, err = e.pipe.GenerateDigest(ctx, time.Now().Add(-time.Duration(*hours)*time.Hour))
		if err != nil {
			return err
		}
		fmt.Printf("generated in %s with %s over %d threads\n", time.Since(start).Round(time.Second), v.Digest.Model, v.Digest.ThreadCount)
	} else {
		ds, err := e.pipe.Digests(ctx, 1)
		if err != nil {
			return err
		}
		if len(ds) == 0 {
			return errors.New("no digest yet; run: gitinbox digest --generate")
		}
		v = ds[0]
		fmt.Printf("digest #%d for %s → %s (%s, %d threads)\n", v.Digest.ID, v.Digest.PeriodStart.Local().Format("Jan 02 15:04"), v.Digest.PeriodEnd.Local().Format("Jan 02 15:04"), v.Digest.Model, v.Digest.ThreadCount)
	}
	fmt.Printf("\n%s\n", v.Content.Headline)
	for _, sec := range v.Content.Sections {
		fmt.Printf("\n## %s\n", sec.Title)
		for _, it := range sec.Items {
			fmt.Printf("  - %s — %s: %s\n", it.Ref, it.Title, it.Why)
		}
	}
	if len(v.Content.SuggestedActions) > 0 {
		fmt.Println("\nSuggested actions:")
		for _, a := range v.Content.SuggestedActions {
			fmt.Printf("  * %s\n", a)
		}
	}
	return nil
}

func cmdUsage(ctx context.Context) error {
	e, err := openEnv(false)
	if err != nil {
		return err
	}
	defer e.close()
	rows, err := e.db.UsageStats(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "SOURCE\tPROVIDER\tMODEL\tCOUNT\tTOKENS IN\tTOKENS OUT\tAVG LATENCY\n")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%.0fms\n", r.Source, r.Provider, r.Model, r.Count, r.InputTokens, r.OutputTokens, r.AvgLatencyMs)
	}
	w.Flush()
	return nil
}

func cmdEval(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("eval: want queue|label|pr-label|judge|report|tune|export|import")
	}
	sub := args[0]
	fs, login := newFlags("eval " + sub)
	limit := fs.Int("limit", 30, "max items")
	provider := fs.String("provider", "", "jev or ollama")
	model := fs.String("model", "", "model override")
	force := fs.Bool("force", false, "re-judge even if already judged at this version")
	iters := fs.Int("iters", 400, "tuning iterations")
	apply := fs.Bool("apply", false, "store the tuned weights")
	out := fs.String("out", "eval", "report directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	e, err := openEnv(sub == "judge" || sub == "label")
	if err != nil {
		return err
	}
	defer e.close()
	switch sub {
	case "queue":
		var acctID int64
		if *login != "" {
			acct, err := e.account(ctx, *login)
			if err != nil {
				return err
			}
			acctID = acct.ID
		}
		items, err := e.pipe.LabelQueue(ctx, acctID, *limit, false)
		if err != nil {
			return err
		}
		for _, it := range items {
			t := it.Thread
			cat := ""
			if it.Score.Judged {
				cat = " judged:" + it.Score.Category
			}
			fmt.Printf("%3d%%%-28s %-12s %-26s #%-6d %s  (%s, %s)\n", it.Score.Percent, cat, t.ThreadID, trunc(t.Repo, 26), t.SubjectNumber, trunc(t.Title, 60), t.ActivityKind, t.Reason)
		}
		fmt.Printf("%d to label\n", len(items))
		return nil
	case "label":
		if fs.NArg() < 2 {
			return errors.New("eval label: want THREAD_ID KEY=VALUE…")
		}
		acct, err := e.account(ctx, *login)
		if err != nil {
			return err
		}
		l, err := e.db.GetLabel(ctx, acct.ID, fs.Arg(0))
		if err != nil {
			l = store.Label{AccountID: acct.ID, ThreadID: fs.Arg(0), Urgency: -1, Relevance: -1, Priority: -1}
		}
		for _, kv := range fs.Args()[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("want KEY=VALUE, got %q", kv)
			}
			b := v == "y" || v == "yes" || v == "true" || v == "1"
			switch k {
			case "category":
				l.Category = v
			case "requires_action", "action":
				l.RequiresAction = &b
			case "resolved":
				l.Resolved = &b
			case "noise":
				l.Noise = &b
			case "note":
				l.Note = v
			case "urgency":
				fmt.Sscanf(v, "%d", &l.Urgency)
			case "relevance":
				fmt.Sscanf(v, "%d", &l.Relevance)
			case "priority":
				fmt.Sscanf(v, "%d", &l.Priority)
			default:
				return fmt.Errorf("unknown label key %q", k)
			}
		}
		if err := e.pipe.SetLabel(ctx, l); err != nil {
			return err
		}
		fmt.Printf("labeled %s\n", fs.Arg(0))
		return nil
	case "pr-label":
		if fs.NArg() < 2 {
			return errors.New("eval pr-label: want REPO#N impact=0-3 [kind=…]")
		}
		acct, err := e.account(ctx, *login)
		if err != nil {
			return err
		}
		repo, num, err := parseRef(fs.Arg(0))
		if err != nil {
			return err
		}
		l := store.PRLabel{AccountID: acct.ID, Repo: repo, Number: num, ImpactLevel: -1}
		for _, kv := range fs.Args()[1:] {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "impact":
				fmt.Sscanf(v, "%d", &l.ImpactLevel)
			case "kind":
				l.ChangeKind = v
			case "note":
				l.Note = v
			}
		}
		return e.pipe.SetPRLabel(ctx, l)
	case "judge":
		if *provider == "" {
			return errors.New("eval judge: --provider jev|ollama is required")
		}
		rep, err := e.pipe.EvalJudge(ctx, *provider, *model, *force)
		fmt.Printf("%s %s: labeled %d, judged %d, skipped %d, failed %d, tokens %d/%d in %s\n", rep.Provider, rep.Model, rep.Labeled, rep.Judged, rep.Skipped, rep.Failed, rep.Usage.InputTokens, rep.Usage.OutputTokens, rep.Duration.Round(time.Second))
		for _, m := range rep.Errors {
			fmt.Println("  -", m)
		}
		return err
	case "report":
		path, err := e.pipe.WriteReport(ctx, *out)
		if err != nil {
			return err
		}
		md, _ := os.ReadFile(path)
		fmt.Print(string(md))
		fmt.Printf("\n(written to %s)\n", path)
		return nil
	case "tune":
		res, err := e.pipe.Tune(ctx, *iters, *apply)
		if err != nil {
			return err
		}
		fmt.Printf("labeled %d · objective %.3f → %.3f after %d iterations\n", res.Labeled, res.NDCGFrom, res.NDCGTo, res.Iters)
		fmt.Printf("action %.2f→%.2f  urgency %.2f→%.2f  relevance %.2f→%.2f  recency %.2f→%.2f (half-life %.0fh→%.0fh)  resolved %.2f→%.2f (thr %.2f→%.2f)  impact %.2f→%.2f  uncal %.2f→%.2f\n",
			res.Before.Action, res.After.Action, res.Before.Urgency, res.After.Urgency, res.Before.Relevance, res.After.Relevance, res.Before.Recency, res.After.Recency,
			res.Before.RecencyHalfLifeH, res.After.RecencyHalfLifeH, res.Before.Resolved, res.After.Resolved, res.Before.ResolvedThreshold, res.After.ResolvedThreshold,
			res.Before.Impact, res.After.Impact, res.Before.UncalibratedDiscount, res.After.UncalibratedDiscount)
		if *apply {
			fmt.Println("applied" + map[bool]string{true: "", false: " (no improvement; weights unchanged)"}[res.NDCGTo > res.NDCGFrom])
		}
		return nil
	case "export":
		if fs.NArg() < 1 {
			return errors.New("eval export: want FILE")
		}
		f, err := os.Create(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		n, err := e.pipe.ExportLabels(ctx, f)
		fmt.Printf("exported %d labels\n", n)
		return err
	case "import":
		if fs.NArg() < 1 {
			return errors.New("eval import: want FILE")
		}
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		n, err := e.pipe.ImportLabels(ctx, f)
		fmt.Printf("imported %d labels\n", n)
		return err
	}
	return fmt.Errorf("eval: unknown subcommand %q", sub)
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
	fmt.Printf("%s %s: ok (mirrored to %s: %v)", action, id, acct.Host, res.Mirrored)
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
