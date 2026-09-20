// Package localapi is the loopback HTTP JSON API the pi agent's tools call
// (M6). It listens on 127.0.0.1 on a random port, requires a per-process
// bearer token, and exposes the inbox, Mine, impact analyses, forge reads and
// the local triage actions — never a forge write beyond what the account's
// write mode already mirrors.
package localapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitinbox/internal/judge"
	"gitinbox/internal/llm"
	"gitinbox/internal/pipeline"
	"gitinbox/internal/scoring"
	"gitinbox/internal/source"
	"gitinbox/internal/store"
)

// Server is the loopback API.
type Server struct {
	db    *store.DB
	pipe  *pipeline.Pipeline
	log   *slog.Logger
	token string
	ln    net.Listener
	srv   *http.Server
	url   string
}

// New builds a server with a fresh random token. Nothing listens until Start.
func New(db *store.DB, pipe *pipeline.Pipeline, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return &Server{db: db, pipe: pipe, log: logger, token: hex.EncodeToString(b)}
}

// Start listens on 127.0.0.1:0 and serves until Close.
func (s *Server) Start() error {
	if s.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	s.url = "http://" + ln.Addr().String()
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("localapi: serve", "err", err)
		}
	}()
	return nil
}

// URL is the base URL (empty before Start). Token is the bearer token.
func (s *Server) URL() string   { return s.url }
func (s *Server) Token() string { return s.token }

// Close stops the listener.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.srv.Shutdown(ctx)
	s.srv, s.ln, s.url = nil, nil, ""
	return err
}

// Handler is the routed, authenticated handler (exposed for tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/accounts", s.accounts)
	mux.HandleFunc("GET /v1/tools", s.tools)
	mux.HandleFunc("GET /v1/threads", s.threads)
	mux.HandleFunc("GET /v1/threads/find", s.findThread)
	mux.HandleFunc("GET /v1/threads/{account}/{id}", s.thread)
	mux.HandleFunc("GET /v1/threads/{account}/{id}/summary", s.summary)
	mux.HandleFunc("GET /v1/threads/{account}/{id}/comments", s.comments)
	mux.HandleFunc("POST /v1/threads/{account}/{id}/{action}", s.action)
	mux.HandleFunc("GET /v1/mine", s.mine)
	mux.HandleFunc("GET /v1/impact", s.impactList)
	mux.HandleFunc("GET /v1/impact/{account}", s.impact)
	mux.HandleFunc("GET /v1/changes/{account}", s.changes)
	mux.HandleFunc("POST /v1/github/{account}/call", s.githubCall)
	mux.HandleFunc("GET /v1/drafts", s.drafts)
	mux.HandleFunc("POST /v1/drafts", s.putDraft)
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		got, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(s.token)) != 1 {
			fail(w, http.StatusUnauthorized, errors.New("missing or invalid bearer token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- helpers ----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func failErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, err)
	case errors.Is(err, pipeline.ErrNotReadTool), errors.Is(err, source.ErrWritesDisabled):
		fail(w, http.StatusForbidden, err)
	default:
		fail(w, http.StatusInternalServerError, err)
	}
}

func intQ(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func boolQ(r *http.Request, key string) bool {
	v := strings.ToLower(r.URL.Query().Get(key))
	return v == "1" || v == "true" || v == "yes"
}

func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// account resolves the optional account query parameter / path value.
func (s *Server) account(ctx context.Context, ref string) (store.Account, error) {
	return s.pipe.ResolveAccount(ctx, ref)
}

// accountID resolves an optional account filter (0 = all).
func (s *Server) accountID(ctx context.Context, ref string) (int64, error) {
	if strings.TrimSpace(ref) == "" {
		return 0, nil
	}
	a, err := s.account(ctx, ref)
	if err != nil {
		return 0, err
	}
	return a.ID, nil
}

// AccountRef is how accounts appear in responses.
type AccountRef struct {
	ID    int64  `json:"id"`
	Forge string `json:"forge"`
	Login string `json:"login"`
	Host  string `json:"host"`
	Ref   string `json:"ref"` // login@host, accepted anywhere an account is expected
}

func refOf(a store.Account) AccountRef {
	return AccountRef{ID: a.ID, Forge: a.Forge, Login: a.Login, Host: a.Host, Ref: a.Login + "@" + a.Host}
}

// Row is the compact list view of a scored thread.
type Row struct {
	Account      string   `json:"account"`
	ThreadID     string   `json:"thread_id"`
	Forge        string   `json:"forge"`
	Repo         string   `json:"repo"`
	Number       int      `json:"number,omitempty"`
	Kind         string   `json:"kind"` // Issue | PullRequest | MergeRequest | Release | …
	Title        string   `json:"title"`
	ActivityKind string   `json:"activity_kind"`
	Reason       string   `json:"reason"`
	Relations    []string `json:"my_relations"`
	Actor        string   `json:"actor,omitempty"`
	UpdatedAt    string   `json:"updated_at"`
	Unread       bool     `json:"unread"`
	Done         bool     `json:"done"`
	Snoozed      bool     `json:"snoozed"`
	Noise        bool     `json:"noise"`
	Priority     int      `json:"priority_percent"`
	Bucket       string   `json:"bucket"`
	Category     string   `json:"category,omitempty"`
	NextAction   string   `json:"next_action,omitempty"`
	Unsure       bool     `json:"unsure,omitempty"`
	Pinned       bool     `json:"pinned,omitempty"`
	ImpactLevel  string   `json:"impact_level,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	URL          string   `json:"url"`
}

var levelNames = []string{"none", "possible", "likely", "certain"}

func levelName(l int) string {
	if l >= 0 && l < len(levelNames) {
		return levelNames[l]
	}
	return ""
}

func (s *Server) rows(ctx context.Context, scored []pipeline.Scored) []Row {
	accts := map[int64]store.Account{}
	if list, err := s.db.ListAccounts(ctx); err == nil {
		for _, a := range list {
			accts[a.ID] = a
		}
	}
	out := make([]Row, 0, len(scored))
	for _, sc := range scored {
		t := sc.Thread
		a := accts[t.AccountID]
		r := Row{Account: a.Login + "@" + a.Host, ThreadID: t.ThreadID, Forge: a.Forge, Repo: t.Repo, Number: t.SubjectNumber, Kind: t.SubjectType, Title: t.Title,
			ActivityKind: t.ActivityKind, Reason: t.Reason, Relations: t.RelationTags, Actor: t.Actor, UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
			Unread: !t.IsRead(), Done: t.DoneAt != nil, Snoozed: t.SnoozedUntil != nil && t.SnoozedUntil.After(time.Now()), Noise: t.FilterVerdict == "noise",
			Priority: sc.Score.Percent, Bucket: sc.Score.Bucket, Category: sc.Score.Category, NextAction: sc.Score.NextAction, Unsure: sc.Score.Unsure, Pinned: sc.Score.Pinned,
			ImpactLevel: levelName(sc.Score.ImpactLevel), Summary: sc.Summary, URL: t.HTMLURL}
		if r.Relations == nil {
			r.Relations = []string{}
		}
		out = append(out, r)
	}
	return out
}

// --- handlers ---------------------------------------------------------------

func (s *Server) accounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.ListAccounts(r.Context())
	if err != nil {
		failErr(w, err)
		return
	}
	out := make([]AccountRef, 0, len(list))
	for _, a := range list {
		out = append(out, refOf(a))
	}
	writeJSON(w, 200, map[string]any{"accounts": out})
}

func (s *Server) tools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"github_read_tools": pipeline.ReadTools()})
}

func (s *Server) threads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := s.accountID(ctx, r.URL.Query().Get("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := intQ(r, "limit", 0)
	var scored []pipeline.Scored
	if q == "" {
		bucket := r.URL.Query().Get("bucket")
		if bucket == "all" {
			bucket = ""
		}
		scored, err = s.pipe.TopThreads(ctx, accountID, bucket, limit)
	} else {
		scored, err = s.pipe.SearchThreads(ctx, accountID, q, boolQ(r, "include_read"), limit)
	}
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"threads": s.rows(ctx, scored)})
}

func (s *Server) findThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct, err := s.account(ctx, r.URL.Query().Get("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	repo := r.URL.Query().Get("repo")
	number := intQ(r, "number", 0)
	if repo == "" || number <= 0 {
		fail(w, 400, errors.New("repo and number are required"))
		return
	}
	t, err := s.pipe.FindThread(ctx, acct.ID, repo, number)
	if err != nil {
		failErr(w, err)
		return
	}
	s.writeThread(w, r, acct, t.ThreadID)
}

// ThreadDetail is the full view of one thread for the agent.
type ThreadDetail struct {
	Account       AccountRef              `json:"account"`
	Row           Row                     `json:"thread"`
	State         judge.TriageState       `json:"state"` // body, latest activity, my relation — what the judge saw
	Score         scoring.Result          `json:"score"`
	Answers       map[string]judge.Answer `json:"judgment,omitempty"`
	JudgmentStale bool                    `json:"judgment_stale,omitempty"`
	Provider      string                  `json:"judge_provider,omitempty"`
	Summary       *llm.ThreadSummary      `json:"summary,omitempty"`
	SummaryStale  bool                    `json:"summary_stale,omitempty"`
	Impact        *ImpactRow              `json:"impact,omitempty"`
	Draft         *store.Draft            `json:"draft,omitempty"`
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request) {
	acct, err := s.account(r.Context(), r.PathValue("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.writeThread(w, r, acct, r.PathValue("id"))
}

func (s *Server) writeThread(w http.ResponseWriter, r *http.Request, acct store.Account, threadID string) {
	ctx := r.Context()
	ex, err := s.pipe.Explain(ctx, acct, threadID, false)
	if err != nil {
		failErr(w, err)
		return
	}
	scored, err := s.pipe.ScoreThreads(ctx, []store.Thread{ex.Thread})
	if err != nil || len(scored) == 0 {
		failErr(w, fmt.Errorf("score: %v", err))
		return
	}
	d := ThreadDetail{Account: refOf(acct), Row: s.rows(ctx, scored)[0], State: ex.State, Score: ex.Score, JudgmentStale: ex.Stale}
	if ex.Judgment != nil {
		d.Answers = ex.Answers
		d.Provider = ex.Judgment.Provider + "/" + ex.Judgment.Model
	}
	if sv, err := s.pipe.Summary(ctx, acct, threadID); err == nil {
		c := sv.Content
		d.Summary = &c
		d.SummaryStale = sv.Stale
	}
	if ex.Impact != nil {
		ir := impactRow(*ex.Impact)
		d.Impact = &ir
	}
	if dr, err := s.db.GetDraft(ctx, acct.ID, threadID); err == nil {
		d.Draft = &dr
	}
	writeJSON(w, 200, d)
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct, err := s.account(ctx, r.PathValue("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	id := r.PathValue("id")
	var sv pipeline.SummaryView
	if boolQ(r, "generate") {
		sv, err = s.pipe.Summarize(ctx, acct, id, boolQ(r, "force"))
	} else {
		sv, err = s.pipe.Summary(ctx, acct, id)
		if errors.Is(err, store.ErrNotFound) {
			sv, err = s.pipe.Summarize(ctx, acct, id, false)
		}
	}
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"summary": sv.Content, "stale": sv.Stale, "model": sv.Summary.Model, "created_at": sv.Summary.CreatedAt})
}

func (s *Server) comments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct, err := s.account(ctx, r.PathValue("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	t, err := s.db.GetThread(ctx, acct.ID, r.PathValue("id"))
	if err != nil {
		failErr(w, err)
		return
	}
	if t.SubjectNumber == 0 {
		fail(w, 400, errors.New("this thread is not an issue or pull/merge request"))
		return
	}
	list, err := s.pipe.Comments(ctx, acct, t.Repo, t.SubjectNumber, t.SubjectType, intQ(r, "limit", 30))
	if err != nil {
		failErr(w, err)
		return
	}
	if list == nil {
		list = []source.Comment{}
	}
	writeJSON(w, 200, map[string]any{"repo": t.Repo, "number": t.SubjectNumber, "kind": t.SubjectType, "title": t.Title, "item_body": t.ItemBody, "item_author": t.ItemAuthor, "state": t.ItemState, "comments": list})
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct, err := s.account(ctx, r.PathValue("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	id := r.PathValue("id")
	if _, err := s.db.GetThread(ctx, acct.ID, id); err != nil {
		failErr(w, err)
		return
	}
	var body struct {
		Hours int `json:"hours"`
	}
	if err := decodeBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	var res pipeline.MirrorResult
	switch r.PathValue("action") {
	case "read":
		res, err = s.pipe.MarkRead(ctx, acct, id)
	case "done":
		res, err = s.pipe.MarkDone(ctx, acct, id)
	case "undone":
		err = s.pipe.UndoDone(ctx, acct, id)
	case "snooze":
		h := body.Hours
		if h <= 0 {
			h = 24
		}
		err = s.pipe.Snooze(ctx, acct, id, time.Now().Add(time.Duration(h)*time.Hour))
	case "mute":
		res, err = s.pipe.Unsubscribe(ctx, acct, id)
	default:
		fail(w, 404, fmt.Errorf("unknown action %q (read|done|undone|snooze|mute)", r.PathValue("action")))
		return
	}
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "mirrored_to_forge": res.Mirrored, "warning": res.Warning})
}

func (s *Server) mine(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := s.accountID(ctx, r.URL.Query().Get("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	var accts []store.Account
	if accountID != 0 {
		a, err := s.db.GetAccount(ctx, accountID)
		if err != nil {
			failErr(w, err)
			return
		}
		accts = []store.Account{a}
	} else if accts, err = s.db.ListAccounts(ctx); err != nil {
		failErr(w, err)
		return
	}
	type item struct {
		Account string `json:"account"`
		store.Item
	}
	out := []item{}
	for _, a := range accts {
		items, err := s.db.ListItems(ctx, a.ID, boolQ(r, "closed"))
		if err != nil {
			failErr(w, err)
			return
		}
		for _, it := range items {
			out = append(out, item{Account: a.Login + "@" + a.Host, Item: it})
		}
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

// ImpactRow is the agent-sized view of an analysis.
type ImpactRow struct {
	Repo        string          `json:"repo"`
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	State       string          `json:"state"`
	URL         string          `json:"url"`
	Author      string          `json:"author,omitempty"`
	ImpactLevel string          `json:"impact_level"`
	ChangeKind  string          `json:"change_kind"`
	Profile     string          `json:"profile"`
	Layers      []string        `json:"layers"`
	Signals     []string        `json:"signals,omitempty"`
	SurfaceHits int             `json:"surface_hits"`
	Answers     map[string]any  `json:"judgment,omitempty"`
	Note        *llm.ImpactNote `json:"note,omitempty"`
	AnalysedAt  *time.Time      `json:"analysed_at,omitempty"`
}

func impactRow(v pipeline.ImpactView) ImpactRow {
	a := v.Analysis
	row := ImpactRow{Repo: a.Repo, Number: a.Number, Title: a.Title, State: a.State, URL: a.HTMLURL, Author: a.Author, ImpactLevel: levelName(a.ImpactLevel), ChangeKind: a.ChangeKind,
		Profile: a.ProfileID, Layers: []string{}, Signals: v.Report.Signals, SurfaceHits: len(v.Report.SurfaceHits), Note: v.Note, AnalysedAt: a.AnalysedAt}
	for _, l := range v.Report.Layers {
		row.Layers = append(row.Layers, l.ID)
	}
	if len(v.Answers) > 0 {
		row.Answers = map[string]any{}
		for id, ans := range v.Answers {
			switch ans.Kind {
			case judge.Noul:
				row.Answers[id] = ans.Noul
			case judge.Choice:
				row.Answers[id] = map[string]any{"choice": ans.Choice, "confidence": ans.Confidence}
			case judge.Score:
				row.Answers[id] = map[string]any{"score": ans.Score, "confidence": ans.Confidence}
			}
		}
	}
	return row
}

func (s *Server) impactList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := s.accountID(ctx, r.URL.Query().Get("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	q := store.AnalysisQuery{AccountID: accountID, MinLevel: intQ(r, "min", 1), Limit: intQ(r, "limit", 30), Any: true}
	if v := r.URL.Query().Get("landed"); v != "" {
		q.Any = false
		q.Landed = boolQ(r, "landed")
	}
	views, err := s.pipe.ListImpact(ctx, q)
	if err != nil {
		failErr(w, err)
		return
	}
	out := make([]ImpactRow, 0, len(views))
	for _, v := range views {
		row := impactRow(v)
		row.Note = nil // keep the list light; get_pr_analysis returns the note
		out = append(out, row)
	}
	writeJSON(w, 200, map[string]any{"analyses": out})
}

func (s *Server) impact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct, err := s.account(ctx, r.PathValue("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	repo, number := r.URL.Query().Get("repo"), intQ(r, "number", 0)
	if repo == "" || number <= 0 {
		fail(w, 400, errors.New("repo and number are required"))
		return
	}
	v, err := s.pipe.GetImpact(ctx, acct.ID, repo, number)
	if err != nil {
		failErr(w, err)
		return
	}
	if v == nil || boolQ(r, "analyze") {
		if _, _, err := s.pipe.AnalyzePR(ctx, acct, repo, number, r.URL.Query().Get("profile"), boolQ(r, "force")); err != nil {
			failErr(w, err)
			return
		}
		if v, err = s.pipe.GetImpact(ctx, acct.ID, repo, number); err != nil || v == nil {
			failErr(w, fmt.Errorf("analysis not stored: %v", err))
			return
		}
	}
	if boolQ(r, "note") && v.Note == nil {
		if _, err := s.pipe.ImpactNote(ctx, acct, repo, number); err != nil {
			failErr(w, err)
			return
		}
		if v, err = s.pipe.GetImpact(ctx, acct.ID, repo, number); err != nil || v == nil {
			failErr(w, fmt.Errorf("analysis not stored: %v", err))
			return
		}
	}
	writeJSON(w, 200, impactRow(*v))
}

func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct, err := s.account(ctx, r.PathValue("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	repo, number := r.URL.Query().Get("repo"), intQ(r, "number", 0)
	if repo == "" || number <= 0 {
		fail(w, 400, errors.New("repo and number are required"))
		return
	}
	cs, err := s.pipe.Changes(ctx, acct, repo, number)
	if err != nil {
		failErr(w, err)
		return
	}
	maxFiles := intQ(r, "max_files", 40)
	withPatches := boolQ(r, "patches")
	truncated := cs.Truncated
	if len(cs.Files) > maxFiles {
		cs.Files = cs.Files[:maxFiles]
		truncated = true
	}
	budget := 60000
	for i := range cs.Files {
		if !withPatches {
			cs.Files[i].Patch = ""
			continue
		}
		if len(cs.Files[i].Patch) > budget {
			cs.Files[i].Patch = source.TrimText(cs.Files[i].Patch, budget)
			truncated = true
		}
		budget -= len(cs.Files[i].Patch)
		if budget < 0 {
			budget = 0
		}
	}
	cs.Truncated = truncated
	writeJSON(w, 200, cs)
}

func (s *Server) githubCall(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct, err := s.account(ctx, r.PathValue("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	var body struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := decodeBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	if body.Tool == "" {
		fail(w, 400, errors.New("tool is required"))
		return
	}
	text, err := s.pipe.GitHubRead(ctx, acct, body.Tool, body.Args)
	if err != nil {
		failErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if json.Valid([]byte(text)) {
		_, _ = io.WriteString(w, text)
		return
	}
	writeJSON(w, 200, map[string]any{"text": text})
}

func (s *Server) drafts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := s.accountID(ctx, r.URL.Query().Get("account"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	list, err := s.db.ListDrafts(ctx, accountID)
	if err != nil {
		failErr(w, err)
		return
	}
	if list == nil {
		list = []store.Draft{}
	}
	writeJSON(w, 200, map[string]any{"drafts": list})
}

func (s *Server) putDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Account  string `json:"account"`
		ThreadID string `json:"thread_id"`
		Text     string `json:"text"`
		Model    string `json:"model"`
	}
	if err := decodeBody(r, &body); err != nil {
		fail(w, 400, err)
		return
	}
	acct, err := s.account(ctx, body.Account)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if strings.TrimSpace(body.Text) == "" || body.ThreadID == "" {
		fail(w, 400, errors.New("thread_id and text are required"))
		return
	}
	t, err := s.db.GetThread(ctx, acct.ID, body.ThreadID)
	if err != nil {
		failErr(w, err)
		return
	}
	d := store.Draft{AccountID: acct.ID, ThreadID: body.ThreadID, Repo: t.Repo, Number: t.SubjectNumber, Text: body.Text, Model: body.Model, CreatedAt: time.Now()}
	if err := s.db.PutDraft(ctx, d); err != nil {
		failErr(w, err)
		return
	}
	s.pipe.EmitDraftSaved(acct.ID, body.ThreadID)
	writeJSON(w, 200, map[string]any{"ok": true, "stored": "locally only — nothing was posted to " + acct.Forge, "draft": d})
}
