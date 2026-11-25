package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Tips are shown once ever, then never again (DESIGN.md 5.1 rule 6). What
// has been shown is kept in <data dir>/seen.json, written atomically, and
// /help lists the tips so nothing is lost by hiding them.
const (
	tipWelcome = "welcome_hint" // the first-run command line under the wordmark
	tipNoModel = "no_model"     // "! NO MODEL  /help model" on the first plain-text miss
)

// tipText is what /help shows for each tip.
var tipText = map[string]string{
	tipWelcome: "/chart BTC   /vaults   /vault hlp",
	tipNoModel: "[llm] in config.toml to chat",
}

type seenTips struct {
	path string
	m    map[string]bool
}

func loadSeen(dataDir string) *seenTips {
	s := &seenTips{m: map[string]bool{}}
	if dataDir == "" {
		return s
	}
	s.path = filepath.Join(dataDir, "seen.json")
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.m)
		if s.m == nil {
			s.m = map[string]bool{}
		}
	}
	return s
}

// Seen reports whether tip was already shown.
func (s *seenTips) Seen(tip string) bool { return s != nil && s.m[tip] }

// Mark records tip as shown. The file is written only when this changes
// something, so a tip costs one write ever.
func (s *seenTips) Mark(tip string) {
	if s == nil || s.m[tip] {
		return
	}
	s.m[tip] = true
	if s.path == "" {
		return
	}
	b, err := json.Marshal(s.m)
	if err != nil {
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".seen-*.json")
	if err != nil {
		return
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
	}
}

// List returns the tips shown so far, sorted.
func (s *seenTips) List() []string {
	if s == nil {
		return nil
	}
	var out []string
	for k, v := range s.m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
