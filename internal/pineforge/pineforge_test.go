package pineforge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testdata/report_synthetic.json is hand-written in the report layout
// PineForge's harness prints; its numbers are invented.

func TestParseReport(t *testing.T) {
	b, err := os.ReadFile("testdata/report_synthetic.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseReport(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.TotalTrades != 2 || r.Summary.NetPnL != 1.5 || len(r.Trades) != 2 || len(r.EquityCurve) != 2 {
		t.Fatalf("report = %+v", r)
	}
	if tr := r.Trades[1]; tr.Side != "short" || !tr.OpenAtEnd || tr.ExitPrice != 104.5 {
		t.Errorf("trade 2 = %+v", tr)
	}
	if len(r.Metrics) == 0 || len(r.Fingerprint) == 0 {
		t.Error("metrics and fingerprint should be kept raw")
	}
}

func TestParseReportEngineError(t *testing.T) {
	_, err := ParseReport([]byte(`{"engine":"pineforge","error":"request.security(...) at line 3: no data is pinned for this request"}`))
	var e *Error
	if !errors.As(err, &e) || e.Kind != "backtest" || !strings.Contains(e.Msg, "line 3") {
		t.Fatalf("err = %v", err)
	}
	if _, err := ParseReport([]byte("not json")); err == nil {
		t.Fatal("expected an error for non-JSON output")
	}
}

func TestArgs(t *testing.T) {
	t.Setenv(EnvImage, "")
	r := Request{Inputs: map[string]string{"Fast Length": "8"}, Overrides: map[string]string{"initial_capital": "1000"}, InputTF: "15"}
	args, err := r.Args("/tmp/x")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm --pull=never --network=none --cap-drop=ALL --security-opt=no-new-privileges",
		"--memory=2g --memory-swap=2g --pids-limit=256 --cpus=2",
		"--tmpfs /tmp:rw,size=512m",
		"/tmp/x/strategy.pine:/in/strategy.pine:ro",
		"/tmp/x/ohlcv.csv:/in/ohlcv.csv:ro",
		`PINEFORGE_INPUTS={"Fast Length":"8"}`,
		`PINEFORGE_OVERRIDES={"initial_capital":"1000"}`,
		"PINEFORGE_INPUT_TF=15",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q:\n%s", want, got)
		}
	}
	if args[len(args)-1] != DefaultImage {
		t.Errorf("image = %s", args[len(args)-1])
	}
	t.Setenv(EnvImage, "local/pineforge:dev")
	args, _ = Request{}.Args("/tmp/x")
	if args[len(args)-1] != "local/pineforge:dev" {
		t.Errorf("env image ignored: %v", args)
	}
}

func TestWriteOHLCV(t *testing.T) {
	var buf bytes.Buffer
	bars := []Bar{{Time: time.UnixMilli(1735689600000), Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10}}
	if err := WriteOHLCV(&buf, bars); err != nil {
		t.Fatal(err)
	}
	want := "timestamp,open,high,low,close,volume\n1735689600000,1,2,0.5,1.5,10\n"
	if buf.String() != want {
		t.Fatalf("got %q", buf.String())
	}
}

// fakeDocker writes a script that stands in for the container CLI.
func fakeDocker(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stand-in")
	}
	p := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunWithStandIn(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/report_synthetic.json")
	docker := fakeDocker(t, `
for a in "$@"; do case "$a" in *:/in/ohlcv.csv:ro) f="${a%%:/in/ohlcv.csv:ro}";; esac; done
head -1 "$f" | grep -q '^timestamp,open,high,low,close,volume$' || { echo bad csv >&2; exit 2; }
cat "`+fixture+`"`)
	bars := []Bar{{Time: time.UnixMilli(1735689600000), Open: 1, High: 1, Low: 1, Close: 1}}
	r, err := run(context.Background(), Request{Pine: "//@version=6\n", Bars: bars, Docker: docker})
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.TotalTrades != 2 {
		t.Fatalf("summary = %+v", r.Summary)
	}
}

func TestRunClassifiesExitCodes(t *testing.T) {
	docker := fakeDocker(t, `echo 'unsupported construct at 4:2' >&2; exit 5`)
	bars := []Bar{{Time: time.UnixMilli(0)}}
	_, err := run(context.Background(), Request{Pine: "x", Bars: bars, Docker: docker})
	var e *Error
	if !errors.As(err, &e) || e.Kind != "transpile" || e.Code != 5 || !strings.Contains(e.Stderr, "4:2") {
		t.Fatalf("err = %#v", err)
	}
	docker = fakeDocker(t, `echo '{"engine":"pineforge","error":"script timeframe finer than input"}'; exit 4`)
	_, err = run(context.Background(), Request{Pine: "x", Bars: bars, Docker: docker})
	if !errors.As(err, &e) || e.Kind != "backtest" || e.Msg != "script timeframe finer than input" {
		t.Fatalf("err = %#v", err)
	}
}

func TestRunNotInstalled(t *testing.T) {
	_, err := run(context.Background(), Request{Docker: "hlp-no-such-docker-binary", Bars: []Bar{{}}})
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

func TestArgsRejectsFlagLikeImage(t *testing.T) {
	for _, img := range []string{"--privileged", "-v", "img with space"} {
		if _, err := (Request{Image: img}).Args(t.TempDir()); err == nil {
			t.Errorf("image %q accepted", img)
		}
	}
	t.Setenv(EnvImage, "--network=host")
	if _, err := (Request{}).Args(t.TempDir()); err == nil {
		t.Error("flag-like $HLP_PINEFORGE_IMAGE accepted")
	}
}
