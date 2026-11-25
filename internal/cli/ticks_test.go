package cli

import (
	"strings"
	"testing"
)

func TestCandlesTickChartCLI(t *testing.T) {
	fx := isolate(t)
	r := runCLI(t, fx, true, "candles", "btc", "--ticks", "1", "--plain", "--height", "16")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	for _, want := range []string{"BTC", "1T", "local history 2 trades", "recording"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("missing %q:\n%s", want, r.stdout)
		}
	}
	// JSON: the bar spec from human numbers, history says local only
	r = runCLI(t, fx, false, "candles", "BTC", "--usd-bars", "1m", "--json")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	data, _ := decodeEnvelope(t, r.stdout)
	d := data.(map[string]any)
	bar := d["bar"].(map[string]any)
	if bar["kind"] != "usd" || bar["size"].(float64) != 1e6 || d["label"] != "$1M" {
		t.Fatalf("bar %v label %v", bar, d["label"])
	}
	if h := d["history"].(map[string]any); h["localOnly"] != true || h["trades"].(float64) != 2 {
		t.Fatalf("history %v", h)
	}
	// two bar types is a usage error (exit 2)
	if r := runCLI(t, fx, false, "candles", "BTC", "--ticks", "100", "--range-bars", "5"); r.code != 2 || errCode(t, r.stderr) != "USAGE" {
		t.Fatalf("exit %d %s", r.code, r.stderr)
	}
	// hlp ticks lists the stored coin
	r = runCLI(t, fx, true, "ticks", "--plain")
	if r.code != 0 || !strings.Contains(r.stdout, "BTC") || !strings.Contains(r.stdout, "local only") {
		t.Fatalf("ticks: %d %s %s", r.code, r.stdout, r.stderr)
	}
}

func TestTickReplayCLI(t *testing.T) {
	fx := isolate(t)
	if r := runCLI(t, fx, false, "candles", "BTC", "--ticks", "1", "--json"); r.code != 0 {
		t.Fatalf("seed: %s", r.stderr)
	}
	// The empty fixtures directory has no registry or market reader data.
	// Replay can only succeed using the recording.
	missing := t.TempDir()
	r := runCLI(t, missing, false, "ticks", "replay", "btc", "--usd-bars", "1m", "--count", "1")
	if r.code != 0 {
		t.Fatal(r.stderr)
	}
	data, meta := decodeEnvelope(t, r.stdout)
	d := data.(map[string]any)
	if meta["source"] != "local" || meta["truncated"] != true || d["truncated"] != true ||
		d["label"] != "$1M" || d["coin"] != "BTC" || d["from"] != d["to"] || len(d["frames"].([]any)) != 1 {
		t.Fatalf("bad replay %s", r.stdout)
	}
	for _, flags := range [][]string{
		{"--count", "2001"}, {"--count", "0"}, {"--from", "200", "--to", "100"},
		{"--ticks", "1.5"}, {"--ticks", "0"}, {"--ticks", "2", "--usd-bars", "3"},
	} {
		r := runCLI(t, missing, false, append([]string{"ticks", "replay", "btc"}, flags...)...)
		if r.code != 2 || errCode(t, r.stderr) != "USAGE" {
			t.Fatalf("bad replay args %v: %d %s", flags, r.code, r.stderr)
		}
	}
}
