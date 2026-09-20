package mcpbin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocateOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fake-mcp")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := Locate(Options{OverridePath: p})
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if info.Source != SourceOverride || info.Path != p {
		t.Fatalf("got %+v", info)
	}
}

func TestLocateOverrideMissing(t *testing.T) {
	_, err := Locate(Options{OverridePath: filepath.Join(t.TempDir(), "nope")})
	if err == nil {
		t.Fatal("expected error for missing override path")
	}
}

func TestLocateBundledOrFallback(t *testing.T) {
	dataDir := t.TempDir()
	info, err := Locate(Options{DataDir: dataDir})
	version, bundledOK := BundledVersion()
	switch {
	case bundledOK:
		if err != nil {
			t.Fatalf("bundled binary present but Locate failed: %v", err)
		}
		if info.Source != SourceBundled || info.Version != version {
			t.Fatalf("got %+v, want bundled %s", info, version)
		}
		if st, err := os.Stat(info.Path); err != nil || st.Mode()&0o111 == 0 {
			t.Fatalf("extracted binary not executable: %v", err)
		}
		// Second call must reuse the extracted file, not re-extract.
		again, err := Locate(Options{DataDir: dataDir})
		if err != nil || again.Path != info.Path {
			t.Fatalf("second Locate: %+v %v", again, err)
		}
	case err == nil:
		if info.Source != SourceSystem {
			t.Fatalf("no bundled binary; expected system source, got %+v", info)
		}
	default:
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	}
}
