// Package agent runs pi (pi.dev) as a sidecar in RPC mode: it prepares an
// app-owned agent directory, spawns `pi --mode rpc`, speaks its JSONL protocol
// and keeps a transcript for the UI (PLAN.md §6, M6).
package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PackageName is the npm package that provides the pi binary.
const PackageName = "@earendil-works/pi-coding-agent"

// Info describes a usable pi binary.
type Info struct {
	Path    string `json:"path"`
	Source  string `json:"source"` // override | path | repo
	Version string `json:"version"`
}

// ErrNotFound is returned when no pi binary could be located.
var ErrNotFound = errors.New("pi not found: install it with `npm install -g " + PackageName + "` (or `cd agent && npm install`) or set the path in Settings → Agent")

// Locate finds pi: the explicit override, then `pi` on PATH, then the
// repository's agent/node_modules (development convenience).
func Locate(override string) (Info, error) {
	if override != "" {
		if err := checkExecutable(override); err != nil {
			return Info{}, err
		}
		return Info{Path: override, Source: "override"}, nil
	}
	if p, err := exec.LookPath("pi"); err == nil {
		return Info{Path: p, Source: "path"}, nil
	}
	var candidates []string
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "agent", "node_modules", ".bin", "pi"))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "..", "agent", "node_modules", ".bin", "pi"), filepath.Join(dir, "agent", "node_modules", ".bin", "pi"))
	}
	for _, c := range candidates {
		if checkExecutable(c) == nil {
			abs, _ := filepath.Abs(c)
			return Info{Path: abs, Source: "repo"}, nil
		}
	}
	return Info{}, ErrNotFound
}

func checkExecutable(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if st.IsDir() || st.Mode()&0o111 == 0 {
		return errors.New(p + " is not executable")
	}
	return nil
}

// Version runs `pi --version` (offline, no update check).
func Version(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = append(os.Environ(), "PI_SKIP_VERSION_CHECK=1", "PI_OFFLINE=1", "PI_TELEMETRY=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", errors.New(strings.TrimSpace(out.String()) + ": " + err.Error())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}
