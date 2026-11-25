package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverridesFixture(t *testing.T) {
	base := mustReg(t)
	data, err := os.ReadFile(filepath.Join("testdata", "overrides.toml"))
	if err != nil {
		t.Fatal(err)
	}
	r, warns, err := base.WithOverrides(data)
	if err != nil {
		t.Fatal(err)
	}
	// one bad table (no address, no slug) is skipped with a warning
	if len(warns) != 2 {
		t.Fatalf("warnings = %v", warns)
	}
	// base untouched
	if e, _ := base.ByID("hlp"); e.HasTag("core") {
		t.Error("base registry mutated")
	}
	hlp, _ := r.ByID("hlp")
	if !hlp.HasTag("core") || hlp.HasTag("flagship") || hlp.Origin != OriginUser || hlp.Notes != "my note" {
		t.Errorf("hlp override = %+v", hlp)
	}
	if hlp.Description == "" {
		t.Error("override cleared an unset field")
	}
	// matched by address
	if e, _ := r.ByID("69-jump-street"); e.Name != "Jump" {
		t.Errorf("address override name = %q", e.Name)
	}
	// hidden
	if _, ok := r.ByID("idx-hlp20"); ok {
		t.Error("hide did not remove idx-hlp20")
	}
	// new user entry: id from name, kind defaulted, user tag allowed
	mine, ok := r.ByID("my-grid-bot")
	if !ok || mine.Kind != KindHyperCore || mine.Origin != OriginUser || !mine.HasTag("my-picks") || mine.SourceURL == "" {
		t.Fatalf("user entry = %+v %v", mine, ok)
	}
	if e, err := r.Resolve("grid bot"); err != nil || e.ID != "my-grid-bot" {
		t.Errorf("Resolve user entry = %v %v", e.ID, err)
	}
	if e, err := r.Resolve("mgb"); err != nil || e.ID != "my-grid-bot" {
		t.Errorf("Resolve alias = %v %v", e.ID, err)
	}
	if TagMeta("my-picks").Group != GroupUser {
		t.Error("user tag group")
	}
	d := r.Discover(Filter{})
	if last := d.Groups[len(d.Groups)-1]; last.Group != GroupUser {
		t.Errorf("user tags group missing, last = %s", last.Group)
	}
	if r.Len() != base.Len() {
		// +1 user entry, -1 hidden
		t.Errorf("len = %d, want %d", r.Len(), base.Len())
	}
}

func TestLoadUser(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	p := UserPath(cfg)
	if p != filepath.Join(dir, "vaults.toml") {
		t.Fatalf("UserPath = %s", p)
	}
	r, w := LoadUser(p) // missing file
	if len(w) != 0 || r.Len() != 39 {
		t.Fatalf("missing file: %d %v", r.Len(), w)
	}
	if err := os.WriteFile(p, []byte("this is = = not toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, w = LoadUser(p)
	if len(w) != 1 || !strings.Contains(w[0], "built-in") || r.Len() != 39 {
		t.Fatalf("broken file: %d %v", r.Len(), w)
	}
	if err := os.WriteFile(p, []byte("[[vault]]\nid = \"hlp\"\nhide = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, w = LoadUser(p)
	if len(w) != 0 || r.Len() != 38 {
		t.Fatalf("hide file: %d %v", r.Len(), w)
	}
}
