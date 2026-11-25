package paperx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Store keeps paper accounts as JSON files under Dir (one per id, written
// atomically) and serializes every read-modify-write with a per-account
// lock file, so a CLI order and a running feed never lose each other's
// changes.
type Store struct {
	Dir string // <data dir>/paper/accounts
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidID checks an account id: lowercase letters, digits, _ and -, at
// most 32 characters.
func ValidID(id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("paper account id must be 1-32 lowercase letters, digits, _ or - (got %q)", id)
	}
	return nil
}

func (s Store) path(id string) string { return filepath.Join(s.Dir, id+".json") }

// ErrNoAccount is returned by Load for an id that was never created.
var ErrNoAccount = errors.New("paper account not found")

// Load reads an account.
func (s Store) Load(id string) (*State, error) {
	if err := ValidID(id); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("paper account %s: %w", id, err)
	}
	st.init()
	return &st, nil
}

// Save writes an account atomically (0600).
func (s Store) Save(st *State) error {
	if err := ValidID(st.ID); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "."+st.ID+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, s.path(st.ID))
}

// List returns the account ids, sorted.
func (s Store) List() ([]string, error) {
	ents, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".") {
			out = append(out, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Update locks id, loads it (creating it with create() when missing and
// create is non-nil), runs f and saves the result unless f fails.
func (s Store) Update(id string, create func() *State, f func(*State) error) (*State, error) {
	if err := ValidID(id); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	unlock, err := s.lock(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	st, err := s.Load(id)
	if errors.Is(err, ErrNoAccount) && create != nil {
		st, err = create(), nil
	}
	if err != nil {
		return nil, err
	}
	if err := f(st); err != nil {
		return st, err
	}
	return st, s.Save(st)
}

func (s Store) lock(id string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.Dir, "."+id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	unlock := lockFile(f)
	return func() { unlock(); f.Close() }, nil
}
