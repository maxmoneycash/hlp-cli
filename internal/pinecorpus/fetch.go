package pinecorpus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The corpus is fetched at test time, never committed (see doc.go).
const (
	// Repo is the corpus repository.
	Repo = "https://github.com/pineforge-4pass/pineforge-corpus"
	// Commit pins the corpus revision the scoreboard in docs/PINEFORGE.md
	// was produced from (2026-11-09).
	Commit = "dc8027362ff11faad1e6646f72e02e04d06c96f2"
	// EnvCache overrides the cache directory Fetch clones into.
	EnvCache = "HLP_PINECORPUS_CACHE"
	// EnvEnable gates every test that downloads the corpus.
	EnvEnable = "HLP_CORPUS"
)

// sparsePatterns check out the probes (minus generated.cpp, which hlp does
// not need), the 1m feed and the licence files: about 570 MB on disk (with
// the derived 15m feed) of the repository's ~600.
var sparsePatterns = []string{
	"/validation/", "!/validation/*/generated.cpp",
	"/data/", "/LICENSE", "/NOTICE", "/LEGAL.md", "/README.md",
}

// CacheDir is where Fetch keeps checkouts: $HLP_PINECORPUS_CACHE, else
// <user cache dir>/hlp/pinecorpus.
func CacheDir() (string, error) {
	if v := os.Getenv(EnvCache); v != "" {
		return v, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "hlp", "pinecorpus"), nil
}

// Root returns a corpus checkout: $HLP_PINECORPUS_DIR when set, else a
// pinned checkout under CacheDir, fetched on first use. It needs git and
// git-lfs on PATH and network access the first time.
func Root(ctx context.Context) (string, error) {
	if d := Dir(); d != "" {
		return d, nil
	}
	cache, err := CacheDir()
	if err != nil {
		return "", err
	}
	return Fetch(ctx, filepath.Join(cache, Commit[:12]), Commit)
}

// MinFreeBytes is the free space Fetch requires before downloading (the
// sparse checkout with the LFS feed is about 570 MB; keep headroom).
const MinFreeBytes = 2 << 30

// safeFetchDir refuses to (re)create dir unless it is CacheDir()/<commit
// prefix>: Fetch deletes dir before cloning, so a caller-supplied path such
// as a home directory must never reach os.RemoveAll.
func safeFetchDir(dir, commit string) error {
	cache, err := CacheDir()
	if err != nil {
		return err
	}
	cache, _ = filepath.Abs(cache)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if len(commit) < 12 || filepath.Base(abs) != commit[:12] || filepath.Dir(abs) != filepath.Clean(cache) {
		return fmt.Errorf("pinecorpus: refusing to replace %s: Fetch only manages %s", dir, filepath.Join(cache, "<commit[:12]>"))
	}
	return nil
}

// Fetch makes a shallow, sparse checkout of the corpus at commit in dir
// (reusing it when it is already there) and pulls the Git LFS feed. All
// commands run without a shell.
func Fetch(ctx context.Context, dir, commit string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "validation")); err == nil {
		if head, err := git(ctx, dir, nil, "rev-parse", "HEAD"); err == nil && strings.TrimSpace(head) == commit {
			if err := ensureFeed(ctx, dir); err != nil {
				return "", err
			}
			return dir, nil
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("pinecorpus: git is not on PATH")
	}
	if _, err := exec.LookPath("git-lfs"); err != nil {
		return "", errors.New("pinecorpus: git-lfs is not on PATH (the 1m feed is stored in Git LFS)")
	}
	if err := safeFetchDir(dir, commit); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if free, ok := freeBytes(filepath.Dir(dir)); ok && free < MinFreeBytes {
		return "", fmt.Errorf("pinecorpus: only %.1f GB free under %s; the corpus needs about %.0f GB (free space or set %s to an existing checkout)",
			float64(free)/1e9, filepath.Dir(dir), float64(MinFreeBytes)/1e9, EnvDir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	noSmudge := []string{"GIT_LFS_SKIP_SMUDGE=1"}
	if _, err := git(ctx, filepath.Dir(dir), noSmudge, "clone", "-q", "--filter=blob:none", "--no-checkout", "--depth", "1", Repo, dir); err != nil {
		return "", err
	}
	steps := [][]string{
		append([]string{"sparse-checkout", "set", "--no-cone"}, sparsePatterns...),
		{"fetch", "-q", "--depth", "1", "origin", commit},
		{"checkout", "-q", "-f", "--detach", commit},
	}
	for _, a := range steps {
		if _, err := git(ctx, dir, noSmudge, a...); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	if err := ensureFeed(ctx, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureFeed replaces the LFS pointer of the 1m feed with the real file.
func ensureFeed(ctx context.Context, dir string) error {
	p := filepath.Join(dir, Feed1m)
	if st, err := os.Stat(p); err == nil && st.Size() > 1<<20 {
		return nil
	}
	if _, err := git(ctx, dir, nil, "lfs", "pull", "--include", Feed1m); err != nil {
		return err
	}
	if st, err := os.Stat(p); err != nil || st.Size() < 1<<20 {
		return fmt.Errorf("pinecorpus: %s is still an LFS pointer after git lfs pull", Feed1m)
	}
	return nil
}

func git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	cmd.WaitDelay = 5 * time.Second
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pinecorpus: git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(tail(stderr.String(), 500)))
	}
	return out.String(), nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
