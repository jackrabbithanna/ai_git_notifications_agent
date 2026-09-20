// Package mcpbin locates the github-mcp-server binary the app talks to over stdio.
//
// Resolution order: an explicit override path (Settings) → the binary bundled into
// this executable at build time (extracted once to the user's data directory) →
// a github-mcp-server found on PATH. The bundled copy is produced by the
// `common:build:mcp` task from the Go tool dependency pinned in go.mod, so the
// server version ships in lockstep with the app.
package mcpbin

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed bin
var bundled embed.FS

const (
	binaryName  = "github-mcp-server"
	versionFile = "bin/VERSION"
)

// Source says where a located binary came from.
type Source string

const (
	SourceOverride Source = "override" // explicit path supplied by the caller
	SourceBundled  Source = "bundled"  // extracted from the embedded copy
	SourceSystem   Source = "system"   // found on PATH
)

// Info describes a usable github-mcp-server binary.
type Info struct {
	Path    string `json:"path"`
	Source  Source `json:"source"`
	Version string `json:"version"` // module version for bundled copies; "" when unknown
}

// Options tunes Locate. All fields are optional.
type Options struct {
	// OverridePath is used verbatim when non-empty (must exist and be executable).
	OverridePath string
	// DataDir is where the bundled binary is extracted. Defaults to
	// $XDG_DATA_HOME/gitinbox/bin (or ~/.local/share/gitinbox/bin).
	DataDir string
}

// ErrNotFound is returned when no binary could be located by any strategy.
var ErrNotFound = errors.New("mcpbin: github-mcp-server not found (not bundled, not on PATH, no override)")

func exeName() string {
	if runtime.GOOS == "windows" {
		return binaryName + ".exe"
	}
	return binaryName
}

// BundledVersion reports the version of the embedded binary, if one was built in.
func BundledVersion() (string, bool) {
	v, err := bundled.ReadFile(versionFile)
	if err != nil {
		return "", false
	}
	if _, err := fs.Stat(bundled, "bin/"+exeName()); err != nil {
		return "", false
	}
	return strings.TrimSpace(string(v)), true
}

// Locate finds a github-mcp-server binary following the package's resolution order.
func Locate(opts Options) (Info, error) {
	if opts.OverridePath != "" {
		if err := checkExecutable(opts.OverridePath); err != nil {
			return Info{}, fmt.Errorf("mcpbin: override path: %w", err)
		}
		return Info{Path: opts.OverridePath, Source: SourceOverride}, nil
	}

	if info, ok, err := extractBundled(opts.DataDir); err != nil {
		return Info{}, err
	} else if ok {
		return info, nil
	}

	if p, err := exec.LookPath(binaryName); err == nil {
		return Info{Path: p, Source: SourceSystem}, nil
	}
	return Info{}, ErrNotFound
}

// extractBundled writes the embedded binary to dataDir (once per content hash)
// and returns its path. ok is false when nothing is bundled.
func extractBundled(dataDir string) (Info, bool, error) {
	version, ok := BundledVersion()
	if !ok {
		return Info{}, false, nil
	}
	content, err := bundled.ReadFile("bin/" + exeName())
	if err != nil {
		return Info{}, false, nil
	}
	if dataDir == "" {
		dataDir, err = defaultDataDir()
		if err != nil {
			return Info{}, false, fmt.Errorf("mcpbin: data dir: %w", err)
		}
	}
	sum := sha256.Sum256(content)
	name := fmt.Sprintf("%s-%s-%s", binaryName, sanitize(version), hex.EncodeToString(sum[:4]))
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dataDir, name)

	if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, content) {
		return Info{Path: target, Source: SourceBundled, Version: version}, true, nil
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return Info{}, false, fmt.Errorf("mcpbin: create %s: %w", dataDir, err)
	}
	tmp, err := os.CreateTemp(dataDir, name+".tmp-*")
	if err != nil {
		return Info{}, false, fmt.Errorf("mcpbin: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return Info{}, false, fmt.Errorf("mcpbin: write: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return Info{}, false, fmt.Errorf("mcpbin: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return Info{}, false, fmt.Errorf("mcpbin: close: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return Info{}, false, fmt.Errorf("mcpbin: install %s: %w", target, err)
	}
	return Info{Path: target, Source: SourceBundled, Version: version}, true, nil
}

func defaultDataDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "gitinbox", "bin"), nil
}

func checkExecutable(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s is a directory", p)
	}
	if runtime.GOOS != "windows" && st.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", p)
	}
	return nil
}

// sanitize keeps a module version safe for use in a file name (e.g. "v1.12.2").
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}
