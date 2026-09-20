// Package secrets stores tokens and API keys outside the database: in the OS
// keyring when one is reachable (Secret Service on Linux), otherwise in a
// 0600 JSON file under the user's config directory.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/zalando/go-keyring"
)

const service = "gitinbox"

// legacyService is the pre-rename keyring service; entries found there are
// copied to the new service the first time they are read.
const legacyService = "ghinbox"

// ErrNotFound is returned by Get for unknown keys.
var ErrNotFound = errors.New("secrets: not found")

// Store is a tiny key/value secret store.
type Store interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
	// Backend names the implementation ("keyring" or "file:<path>") for Diagnostics.
	Backend() string
}

// Open returns the keyring-backed store when the keyring works, else a file store.
func Open() Store {
	if keyringAvailable() {
		return keyringStore{}
	}
	path, err := defaultFilePath()
	if err != nil {
		path = filepath.Join(os.TempDir(), "gitinbox-secrets.json")
	}
	return OpenFile(path)
}

// OpenFile returns a file-backed store at path (used as fallback and in tests).
func OpenFile(path string) Store { return &fileStore{path: path} }

func defaultFilePath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, "gitinbox", "secrets.json")
	// Pre-rename fallback file: move it once, if the new one does not exist.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		old := filepath.Join(base, legacyService, "secrets.json")
		if _, err := os.Stat(old); err == nil {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
				_ = os.Rename(old, path)
			}
		}
	}
	return path, nil
}

// AccountKey is the secret key under which an account's token is stored.
func AccountKey(accountID int64) string { return "account:" + strconv.FormatInt(accountID, 10) }

func keyringAvailable() bool {
	probe := "probe-" + strconv.Itoa(os.Getpid())
	if err := keyring.Set(service, probe, "1"); err != nil {
		return false
	}
	_ = keyring.Delete(service, probe)
	return true
}

type keyringStore struct{}

func (keyringStore) Get(key string) (string, error) {
	v, err := keyring.Get(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		lv, lerr := keyring.Get(legacyService, key)
		if lerr != nil {
			return "", ErrNotFound
		}
		_ = keyring.Set(service, key, lv) // migrate; the old entry is left in place
		return lv, nil
	}
	return v, err
}
func (keyringStore) Set(key, value string) error { return keyring.Set(service, key, value) }
func (keyringStore) Delete(key string) error {
	err := keyring.Delete(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
func (keyringStore) Backend() string { return "keyring" }

type fileStore struct {
	mu   sync.Mutex
	path string
}

func (f *fileStore) Backend() string { return "file:" + f.path }

func (f *fileStore) load() (map[string]string, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("secrets: parse %s: %w", f.path, err)
	}
	return m, nil
}

func (f *fileStore) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *fileStore) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *fileStore) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[key] = value
	return f.save(m)
}

func (f *fileStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, key)
	return f.save(m)
}
