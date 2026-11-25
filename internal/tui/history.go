package tui

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// historyMax bounds the persisted prompt history.
const historyMax = 500

// history is the prompt history (up/down), persisted one entry per line in
// DataDir/history. Multi-line entries are stored with "\n" escaped.
type history struct {
	path    string
	items   []string // oldest first
	idx     int      // browsing position; len(items) = not browsing
	scratch string   // what was typed before browsing started
}

func newHistory(dataDir string) *history {
	h := &history{}
	if dataDir != "" {
		h.path = filepath.Join(dataDir, "history")
		h.load()
	}
	h.idx = len(h.items)
	return h
}

func (h *history) load() {
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			h.items = append(h.items, unescapeHistory(line))
		}
	}
	if len(h.items) > historyMax {
		h.items = h.items[len(h.items)-historyMax:]
	}
}

// Add records an entry (deduping an immediate repeat) and appends it to disk.
// A line that looks like a private key, API key or seed phrase is never
// recorded (audit S10).
func (h *history) Add(s string) {
	s = strings.TrimSpace(s)
	h.idx = len(h.items)
	if s == "" || looksSecret(s) || (len(h.items) > 0 && h.items[len(h.items)-1] == s) {
		return
	}
	h.items = append(h.items, s)
	if len(h.items) > historyMax {
		h.items = h.items[len(h.items)-historyMax:]
	}
	h.idx = len(h.items)
	if h.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(escapeHistory(s) + "\n")
}

// Prev moves back in history; current is the live buffer.
func (h *history) Prev(current string) (string, bool) {
	if len(h.items) == 0 || h.idx == 0 {
		return "", false
	}
	if h.idx == len(h.items) {
		h.scratch = current
	}
	h.idx--
	return h.items[h.idx], true
}

// Next moves forward; past the newest entry it restores the scratch buffer.
func (h *history) Next() (string, bool) {
	if h.idx >= len(h.items) {
		return "", false
	}
	h.idx++
	if h.idx == len(h.items) {
		return h.scratch, true
	}
	return h.items[h.idx], true
}

func escapeHistory(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "\n", `\n`)
}

func unescapeHistory(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var (
	// hexKeyRe is a 32-byte hex secret (an EVM private key), with or
	// without 0x, not part of a longer hex run.
	hexKeyRe = regexp.MustCompile(`(?:^|[^0-9a-fA-F])(?:0x)?[0-9a-fA-F]{64}(?:$|[^0-9a-fA-F])`)
	// apiKeyRe is an OpenAI/Anthropic-style key (sk-…, sk-ant-…).
	apiKeyRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9])sk-[A-Za-z0-9_-]{20,}`)
	// seedWordRe is one BIP-39 English word's shape: 3-8 lowercase letters.
	seedWordRe = regexp.MustCompile(`^[a-z]{3,8}$`)
)

// looksSecret reports whether s holds something that must never reach the
// model or the history file (audit S10): a 64-hex private key, an sk- API
// key, or a 12-24 word seed phrase (BIP-39 shape: 12, 15, 18, 21 or 24
// lowercase words of 3-8 letters and nothing else). Transaction hashes are
// 64 hex too; refusing one costs a retyped prompt, leaking a key costs the
// account.
func looksSecret(s string) bool {
	if hexKeyRe.MatchString(s) || apiKeyRe.MatchString(s) {
		return true
	}
	words := strings.Fields(s)
	switch len(words) {
	case 12, 15, 18, 21, 24:
	default:
		return false
	}
	for _, w := range words {
		if !seedWordRe.MatchString(w) {
			return false
		}
	}
	return true
}

// secretRefusal is shown instead of sending a line looksSecret matched.
const secretRefusal = "that looks like a private key, API key or seed phrase, so it was not sent or saved. Store keys with `hlp auth set`; never paste them into chat."
