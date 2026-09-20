package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secrets.json")
	s := OpenFile(path)
	if _, err := s.Get("account:1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.Set("account:1", "ghp_x"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("account:1"); err != nil || v != "ghp_x" {
		t.Fatalf("got %q, %v", v, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file mode = %o, want 600", st.Mode().Perm())
	}
	if err := s.Delete("account:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("account:1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
