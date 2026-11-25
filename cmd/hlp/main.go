package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/maxmoneycash/hlp/internal/cli"
)

// Set by -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, os.Args[1:], buildInfo())
	stop()
	os.Exit(code)
}

// buildInfo returns the ldflags values, or for `go install ...@latest`
// builds (no ldflags) the module version and VCS stamp Go embeds.
func buildInfo() cli.BuildInfo {
	bi := cli.BuildInfo{Version: version, Commit: commit, Date: date}
	if version != "dev" {
		return bi
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return bi
	}
	return fromBuildInfo(bi, info)
}

// fromBuildInfo fills dev defaults from Go's embedded build info. The module
// version loses its leading "v" so every install path prints the same string
// as GoReleaser's {{ .Version }} and `make build` (0.1.0, never v0.1.0).
func fromBuildInfo(bi cli.BuildInfo, info *debug.BuildInfo) cli.BuildInfo {
	if v := info.Main.Version; v != "" && v != "(devel)" {
		bi.Version = strings.TrimPrefix(v, "v")
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 && bi.Commit == "none" {
				bi.Commit = s.Value[:7]
			}
		case "vcs.time":
			if bi.Date == "unknown" {
				bi.Date = s.Value
			}
		}
	}
	return bi
}
