package main

import (
	"runtime/debug"
	"testing"

	"github.com/maxmoneycash/hlp-cli/internal/cli"
)

func TestFromBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v0.3.1"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef0123456"}, {Key: "vcs.time", Value: "2026-11-15T10:00:00Z"},
	}}
	got := fromBuildInfo(cli.BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}, info)
	if got.Version != "0.3.1" || got.Commit != "abcdef0" || got.Date != "2026-11-15T10:00:00Z" {
		t.Fatalf("%+v", got)
	}
	got = fromBuildInfo(cli.BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}, &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}})
	if got.Version != "dev" {
		t.Fatalf("devel build: %+v", got)
	}
}
