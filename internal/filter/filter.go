// Package filter removes obvious noise from notification threads with plain
// rules before anything reaches a model (PLAN.md §2 "filter"). Rules are user
// editable and stored in settings under filter.Key.
package filter

import (
	"regexp"
	"strings"

	"ghinbox/internal/classify"
)

// Key is the settings key holding Rules as JSON.
const Key = "filter.rules"

// Rules configures the noise filter. Zero value = Defaults() semantics are NOT
// implied; load with Defaults() and overlay user edits.
type Rules struct {
	// DropBotUpdates hides dependency-bot threads (dependabot/renovate style titles).
	DropBotUpdates bool `json:"dropBotUpdates"`
	// DropCISuccess hides ci_activity threads whose title reports success.
	DropCISuccess bool `json:"dropCiSuccess"`
	// DropReleases hides release notifications from repos not in KeepReleaseRepos.
	DropReleases bool `json:"dropReleases"`
	// KeepReleaseRepos are owner/name repos whose releases are always kept.
	KeepReleaseRepos []string `json:"keepReleaseRepos"`
	// MutedRepos are owner/name repos (or "owner/*") whose threads are noise.
	MutedRepos []string `json:"mutedRepos"`
	// MutedTitleKeywords are case-insensitive substrings that mark a title as noise.
	MutedTitleKeywords []string `json:"mutedTitleKeywords"`
}

// Defaults are the rules a fresh install starts with.
func Defaults() Rules {
	return Rules{DropBotUpdates: true, DropCISuccess: true}
}

// Verdict is the outcome for one thread.
type Verdict struct {
	Keep   bool
	Reason string // "" when kept; otherwise bot | ci_success | release | muted_repo | keyword
}

// Input is the subset of thread data the rules look at.
type Input struct {
	Repo   string
	Title  string
	Reason string
	Kind   classify.Kind
}

var (
	botTitle  = regexp.MustCompile(`(?i)^(bump|chore\(deps(-dev)?\)|build\(deps(-dev)?\)|fix\(deps\)|update dependency|update .* to v?\d|\[dependabot\]|\[renovate\]|\[snyk\])`)
	ciSuccess = regexp.MustCompile(`(?i)\b(succeeded|success|successful|passed)\b`)
)

// Apply evaluates the rules for one thread.
func Apply(r Rules, in Input) Verdict {
	for _, m := range r.MutedRepos {
		if matchRepo(m, in.Repo) {
			return Verdict{Reason: "muted_repo"}
		}
	}
	lower := strings.ToLower(in.Title)
	for _, kw := range r.MutedTitleKeywords {
		if kw != "" && strings.Contains(lower, strings.ToLower(kw)) {
			return Verdict{Reason: "keyword"}
		}
	}
	if r.DropBotUpdates && botTitle.MatchString(strings.TrimSpace(in.Title)) {
		return Verdict{Reason: "bot"}
	}
	if r.DropCISuccess && in.Kind == classify.KindCI && ciSuccess.MatchString(in.Title) {
		return Verdict{Reason: "ci_success"}
	}
	if r.DropReleases && in.Kind == classify.KindRelease {
		keep := false
		for _, k := range r.KeepReleaseRepos {
			if matchRepo(k, in.Repo) {
				keep = true
				break
			}
		}
		if !keep {
			return Verdict{Reason: "release"}
		}
	}
	return Verdict{Keep: true}
}

func matchRepo(pattern, repo string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	repo = strings.ToLower(repo)
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(repo, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == repo
}
