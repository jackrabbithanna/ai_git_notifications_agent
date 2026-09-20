// Package profiles defines repo-agnostic "impact profiles" (PLAN.md §4.5): what
// a user builds on top of a project, which paths matter (layers), and which
// diff patterns signal public-surface changes. Analyze turns a change set into
// a deterministic report that the impact judge reasons over.
package profiles

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yaml
var builtin embed.FS

// Layer groups paths that matter for the same reason.
type Layer struct {
	ID     string   `yaml:"id" json:"id"`
	Label  string   `yaml:"label" json:"label"`
	Weight float64  `yaml:"weight" json:"weight"`
	Paths  []string `yaml:"paths" json:"paths"`
}

// SurfacePattern is a regex applied to added/removed diff lines.
type SurfacePattern struct {
	ID      string `yaml:"id" json:"id"`
	Pattern string `yaml:"pattern" json:"pattern"`
	re      *regexp.Regexp
}

// Profile is one project's impact configuration.
type Profile struct {
	ID                    string           `yaml:"id" json:"id"`
	Name                  string           `yaml:"name" json:"name"`
	Repos                 []string         `yaml:"repos" json:"repos"` // owner/name (GitHub), github:owner/name, gitlab:<host>/<path>
	DownstreamDescription string           `yaml:"downstream_description" json:"downstreamDescription"`
	Layers                []Layer          `yaml:"layers" json:"layers"`
	IgnorePaths           []string         `yaml:"ignore_paths" json:"ignorePaths"`
	Signals               []string         `yaml:"signals" json:"signals"`
	SurfacePatterns       []SurfacePattern `yaml:"surface_patterns" json:"surfacePatterns"`

	// Set by the loader, not part of the YAML.
	Source  string `yaml:"-" json:"source"` // builtin | user
	Enabled bool   `yaml:"-" json:"enabled"`
	YAML    string `yaml:"-" json:"yaml"`
}

// GenericID is the fallback profile for repos no profile covers.
const GenericID = "generic"

// Parse decodes and validates a profile; regexes are compiled here.
func Parse(src []byte) (Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(src, &p); err != nil {
		return Profile{}, fmt.Errorf("profile: %w", err)
	}
	p.YAML = string(src)
	if err := p.compile(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (p *Profile) compile() error {
	if p.ID == "" {
		return fmt.Errorf("profile: id is required")
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if len(p.Layers) == 0 {
		return fmt.Errorf("profile %s: at least one layer is required", p.ID)
	}
	seen := map[string]bool{}
	for i := range p.Layers {
		l := &p.Layers[i]
		if l.ID == "" || len(l.Paths) == 0 {
			return fmt.Errorf("profile %s: layer %d needs an id and paths", p.ID, i)
		}
		if seen[l.ID] {
			return fmt.Errorf("profile %s: duplicate layer %q", p.ID, l.ID)
		}
		seen[l.ID] = true
		if l.Label == "" {
			l.Label = l.ID
		}
		if l.Weight <= 0 {
			l.Weight = 0.5
		}
		for _, g := range l.Paths {
			if !doublestar.ValidatePattern(g) {
				return fmt.Errorf("profile %s: layer %s: bad glob %q", p.ID, l.ID, g)
			}
		}
	}
	for _, g := range p.IgnorePaths {
		if !doublestar.ValidatePattern(g) {
			return fmt.Errorf("profile %s: bad ignore glob %q", p.ID, g)
		}
	}
	for i := range p.SurfacePatterns {
		sp := &p.SurfacePatterns[i]
		if sp.ID == "" {
			sp.ID = fmt.Sprintf("pattern_%d", i+1)
		}
		re, err := regexp.Compile(sp.Pattern)
		if err != nil {
			return fmt.Errorf("profile %s: surface pattern %s: %w", p.ID, sp.ID, err)
		}
		sp.re = re
	}
	return nil
}

// Builtins returns the embedded profiles (enabled by default).
func Builtins() ([]Profile, error) {
	entries, err := fs.ReadDir(builtin, "builtin")
	if err != nil {
		return nil, err
	}
	var out []Profile
	for _, e := range entries {
		src, err := builtin.ReadFile("builtin/" + e.Name())
		if err != nil {
			return nil, err
		}
		p, err := Parse(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		p.Source = "builtin"
		p.Enabled = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// MatchesRepo reports whether the profile covers a repository. forge is
// "github" or "gitlab"; host is the account host; repo is owner/name or the
// GitLab project path.
func (p Profile) MatchesRepo(forge, host, repo string) bool {
	repo = strings.ToLower(strings.Trim(repo, "/"))
	host = strings.ToLower(host)
	for _, entry := range p.Repos {
		e := strings.ToLower(strings.TrimSpace(entry))
		switch {
		case strings.HasPrefix(e, "github:"):
			if forge == "github" && strings.TrimPrefix(e, "github:") == repo {
				return true
			}
		case strings.HasPrefix(e, "gitlab:"):
			rest := strings.TrimPrefix(e, "gitlab:")
			if forge != "gitlab" {
				continue
			}
			// gitlab:<host>/<path> or gitlab:<path> (any host)
			if rest == repo || rest == host+"/"+repo {
				return true
			}
		default:
			if forge == "github" && e == repo {
				return true
			}
			if forge == "gitlab" && (e == repo || e == host+"/"+repo) {
				return true
			}
		}
	}
	return false
}

// Ignored reports whether a path is excluded from analysis.
func (p Profile) Ignored(path string) bool {
	for _, g := range p.IgnorePaths {
		if ok, _ := doublestar.Match(g, path); ok {
			return true
		}
	}
	return false
}

// LayerFor returns the first layer whose globs match the path.
func (p Profile) LayerFor(path string) (Layer, bool) {
	for _, l := range p.Layers {
		for _, g := range l.Paths {
			if ok, _ := doublestar.Match(g, path); ok {
				return l, true
			}
		}
	}
	return Layer{}, false
}

// --- analysis ---------------------------------------------------------------

// FileChange is one changed file with its (possibly truncated) unified patch.
type FileChange struct {
	Path      string `json:"path"`
	Status    string `json:"status"` // added | modified | removed | renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
}

// LayerHit summarises the files that landed in one layer.
type LayerHit struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Weight    float64  `json:"weight"`
	Files     []string `json:"files"`
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
}

// SurfaceHit is one diff line that matched a surface pattern.
type SurfaceHit struct {
	PatternID string `json:"patternId"`
	Path      string `json:"path"`
	Line      string `json:"line"`
}

// Report is the deterministic analysis of a change set against a profile.
type Report struct {
	ProfileID    string       `json:"profileId"`
	Layers       []LayerHit   `json:"layers"` // sorted by weight desc
	Unclassified []string     `json:"unclassified"`
	Ignored      int          `json:"ignored"`
	Signals      []string     `json:"signals"`
	SurfaceHits  []SurfaceHit `json:"surfaceHits"`
	MaxWeight    float64      `json:"maxWeight"`
	TopFiles     []FileChange `json:"topFiles"` // highest-weight, largest files with trimmed patches
	TotalFiles   int          `json:"totalFiles"`
}

const (
	maxSurfaceHits   = 40
	maxTopFiles      = 12
	maxPatchPerFile  = 2500
	maxPatchTotal    = 60000
	maxSurfaceLine   = 200
	maxUnclassified  = 30
	maxFilesPerLayer = 25
)

// Analyze classifies files into layers, scans title/body/labels for signals,
// and applies surface patterns to added/removed diff lines.
func Analyze(p Profile, title, body string, labels []string, files []FileChange) Report {
	r := Report{ProfileID: p.ID, TotalFiles: len(files)}
	hits := map[string]*LayerHit{}
	type ranked struct {
		f      FileChange
		weight float64
	}
	var kept []ranked
	for _, f := range files {
		if p.Ignored(f.Path) {
			r.Ignored++
			continue
		}
		l, ok := p.LayerFor(f.Path)
		w := 0.1
		if ok {
			w = l.Weight
			h := hits[l.ID]
			if h == nil {
				h = &LayerHit{ID: l.ID, Label: l.Label, Weight: l.Weight}
				hits[l.ID] = h
			}
			if len(h.Files) < maxFilesPerLayer {
				h.Files = append(h.Files, f.Path)
			}
			h.Additions += f.Additions
			h.Deletions += f.Deletions
			if l.Weight > r.MaxWeight {
				r.MaxWeight = l.Weight
			}
		} else if len(r.Unclassified) < maxUnclassified {
			r.Unclassified = append(r.Unclassified, f.Path)
		}
		kept = append(kept, ranked{f, w})
		// Surface patterns over changed lines only.
		if f.Patch != "" && len(p.SurfacePatterns) > 0 && len(r.SurfaceHits) < maxSurfaceHits {
			for _, line := range strings.Split(f.Patch, "\n") {
				if len(line) < 2 || (line[0] != '+' && line[0] != '-') || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
					continue
				}
				for _, sp := range p.SurfacePatterns {
					if sp.re != nil && sp.re.MatchString(line) {
						r.SurfaceHits = append(r.SurfaceHits, SurfaceHit{PatternID: sp.ID, Path: f.Path, Line: trim(line, maxSurfaceLine)})
						break
					}
				}
				if len(r.SurfaceHits) >= maxSurfaceHits {
					break
				}
			}
		}
	}
	for _, h := range hits {
		r.Layers = append(r.Layers, *h)
	}
	sort.Slice(r.Layers, func(i, j int) bool {
		if r.Layers[i].Weight != r.Layers[j].Weight {
			return r.Layers[i].Weight > r.Layers[j].Weight
		}
		return r.Layers[i].ID < r.Layers[j].ID
	})
	// Signals in title, body and labels.
	hay := strings.ToLower(title + "\n" + body + "\n" + strings.Join(labels, "\n"))
	for _, s := range p.Signals {
		if s != "" && strings.Contains(hay, strings.ToLower(s)) {
			r.Signals = append(r.Signals, s)
		}
	}
	// Top files: heaviest layer first, then largest change; patches trimmed and capped.
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].weight != kept[j].weight {
			return kept[i].weight > kept[j].weight
		}
		return kept[i].f.Additions+kept[i].f.Deletions > kept[j].f.Additions+kept[j].f.Deletions
	})
	total := 0
	for _, k := range kept {
		if len(r.TopFiles) >= maxTopFiles || total >= maxPatchTotal {
			break
		}
		f := k.f
		f.Patch = trim(f.Patch, maxPatchPerFile)
		total += len(f.Patch)
		r.TopFiles = append(r.TopFiles, f)
	}
	return r
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
