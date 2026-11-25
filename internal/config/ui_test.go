package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestUIDefaults: the [ui] keys default to paint, motion on and the floor
// (DESIGN.md section 8), and an empty file keeps them.
func TestUIDefaults(t *testing.T) {
	want := UIConfig{PaintBackground: true, Motion: MotionOn, Floor: true}
	if got := Defaults().UI; got != want {
		t.Fatalf("Defaults().UI = %+v, want %+v", got, want)
	}
	cfg, err := Parse("empty", nil)
	if err != nil || cfg.UI != want {
		t.Fatalf("Parse(empty) = %+v, %v", cfg.UI, err)
	}
}

func TestUIParse(t *testing.T) {
	cfg, err := Parse("ui", []byte("[ui]\npaint_background = false\nmotion = \"off\"\nfloor = false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI != (UIConfig{Motion: MotionOff}) {
		t.Fatalf("UI = %+v", cfg.UI)
	}
	// Only motion is set: the other two keep their defaults.
	cfg, err = Parse("ui", []byte("[ui]\nmotion = \"on\"\n"))
	if err != nil || !cfg.UI.PaintBackground || !cfg.UI.Floor {
		t.Fatalf("partial [ui] = %+v, %v", cfg.UI, err)
	}
}

func TestUIValidate(t *testing.T) {
	_, err := Parse("ui", []byte("[ui]\nmotion = \"slow\"\n"))
	var ce *Error
	if !errors.As(err, &ce) || ce.Key != "ui.motion" {
		t.Fatalf("bad motion: %v", err)
	}
	_, err = Parse("ui", []byte("[ui]\nflor = true\n"))
	if err == nil || !strings.Contains(err.Error(), `"ui.floor"`) {
		t.Fatalf("typo not suggested: %v", err)
	}
}

func TestUISaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := Defaults()
	cfg.UI = UIConfig{PaintBackground: false, Motion: MotionOff, Floor: true}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.UI != cfg.UI {
		t.Fatalf("round trip UI = %+v, want %+v", got.UI, cfg.UI)
	}
}
