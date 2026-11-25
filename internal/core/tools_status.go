package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
	"github.com/maxmoneycash/hlp/internal/evm"
	"github.com/maxmoneycash/hlp/internal/hyperliquid"
	"github.com/maxmoneycash/hlp/internal/hyperliquid/ws"
)

// StatusData is the result of status: one check per dependency.
type StatusData struct {
	OK     bool          `json:"ok"`
	Checks []StatusCheck `json:"checks"`
}

// StatusCheck is one health check (info, ws, limiter, hlpxyz, hyperevm:<id>, config).
type StatusCheck struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// statusTools registers status/doctor: connectivity, limiter budget and
// config checks (COMPETITORS #12).
func statusTools() []Tool {
	return []Tool{
		{Name: "status", Command: "status", Slash: "status", Aliases: []string{"doctor"}, Group: "meta", NewInput: in[struct{}](), Run: runStatus,
			Description: "Health checks: Hyperliquid /info latency, WebSocket handshake, rate-limit budget, hlp.xyz, HyperEVM RPCs and the (redacted) config path. Exit 1 if any check fails.",
			Summary:     "Check connectivity, rate-limit budget and config",
			Examples:    []string{"hlp status", "hlp doctor --json"}},
	}
}

// statusTimeout bounds each network check.
var statusTimeout = 6 * time.Second

// infoCaller is the raw /info call (*hyperliquid.Client).
type infoCaller interface {
	Info(ctx context.Context, req any, out any) error
}

// limited exposes the shared weight limiter (*hyperliquid.Client).
type limited interface {
	Limiter() *hyperliquid.Limiter
}

// runStatus runs every check concurrently, each with its own timeout. The
// requests are few and cheap: one exchangeStatus (weight 2), userRateLimit
// when an account is configured (20), one WS subscription, one hlp.xyz GET
// and eth_chainId on the network's HyperEVM RPCs. When any check fails the
// call is an API_ERROR (exit 1) whose details carry every check.
func runStatus(ctx context.Context, env *Env, _ any) (*Result, error) {
	checks := []func(context.Context, *Env) StatusCheck{
		statusInfo, statusWS, statusUserLimit, statusHLPXYZ, statusHyperEVM, statusConfig,
	}
	out := make([]StatusCheck, len(checks))
	parallel(len(checks), func(i int) {
		cctx, cancel := context.WithTimeout(ctx, statusTimeout)
		defer cancel()
		out[i] = checks[i](cctx, env)
	})
	// The budget is read last, so it includes what the checks spent.
	out = append(out[:2:2], append([]StatusCheck{statusLimiter(ctx, env)}, out[2:]...)...)
	data := StatusData{OK: true}
	var failed []string
	for _, c := range out {
		if c.Name == "" {
			continue // not applicable here
		}
		c.Detail = domain.CleanText(c.Detail)
		data.Checks = append(data.Checks, c)
		if !c.OK {
			data.OK = false
			failed = append(failed, c.Name)
		}
	}
	if !data.OK {
		return nil, &Error{Code: CodeAPI, Message: fmt.Sprintf("%d of %d checks failed: %s", len(failed), len(data.Checks), strings.Join(failed, ", ")),
			Details: data, Hint: "re-run `hlp status` in a minute; a 429 clears within seconds"}
	}
	return &Result{Card: CardStatus, Title: "Status", Data: data, Meta: env.meta("hlp")}, nil
}

func since(t time.Time) int64 { return max(time.Since(t).Milliseconds(), 1) }

func statusInfo(ctx context.Context, env *Env) StatusCheck {
	c := StatusCheck{Name: "info"}
	ic, ok := env.HL.(infoCaller)
	if !ok {
		c.OK, c.Detail = true, "offline: fixtures, no network"
		return c
	}
	start := time.Now()
	var st map[string]any
	if err := ic.Info(ctx, map[string]string{"type": "exchangeStatus"}, &st); err != nil {
		c.Detail = AsError(err).Message
		return c
	}
	c.OK, c.LatencyMs = true, since(start)
	c.Detail = "Hyperliquid /info (" + string(env.Config.Network) + ")"
	return c
}

func statusWS(ctx context.Context, env *Env) StatusCheck {
	c := StatusCheck{Name: "ws"}
	if env.Stream == nil {
		c.OK, c.Detail = true, "offline: no stream"
		return c
	}
	start := time.Now()
	ch, cancel, err := ws.Mids(ctx, env.Stream)
	if err != nil {
		c.Detail = AsError(err).Message
		return c
	}
	defer cancel()
	select {
	case _, ok := <-ch:
		if !ok {
			c.Detail = "stream closed before the first message"
			return c
		}
		c.OK, c.LatencyMs, c.Detail = true, since(start), "first price update received"
	case <-ctx.Done():
		c.Detail = "no message within " + statusTimeout.String()
	}
	return c
}

func statusLimiter(_ context.Context, env *Env) StatusCheck {
	c := StatusCheck{Name: "limiter"}
	l, ok := env.HL.(limited)
	if !ok || l.Limiter() == nil {
		return StatusCheck{} // not applicable offline
	}
	lim := l.Limiter()
	avail := lim.Available()
	scope := "this process"
	if lim.Shared() {
		scope = "shared by hlp processes"
	}
	c.Detail = fmt.Sprintf("%.0f of %.0f weight per minute left (%s)", max(avail, 0), lim.Capacity(), scope)
	c.OK = avail > 0
	if !c.OK {
		c.Detail += "; requests are paused after a 429"
	}
	return c
}

func statusUserLimit(ctx context.Context, env *Env) StatusCheck {
	addr := env.Config.Account
	ic, ok := env.HL.(infoCaller)
	if addr == "" || !ok || !IsAddress(addr) {
		return StatusCheck{}
	}
	c := StatusCheck{Name: "account-limit"}
	start := time.Now()
	var r struct {
		CumVlm        string `json:"cumVlm"`
		NRequestsUsed int64  `json:"nRequestsUsed"`
		NRequestsCap  int64  `json:"nRequestsCap"`
	}
	if err := ic.Info(ctx, map[string]string{"type": "userRateLimit", "user": strings.ToLower(addr)}, &r); err != nil {
		c.Detail = AsError(err).Message
		return c
	}
	c.LatencyMs = since(start)
	c.OK = r.NRequestsCap == 0 || r.NRequestsUsed < r.NRequestsCap
	c.Detail = fmt.Sprintf("%s: %d of %d address actions used", shortAddr(addr), r.NRequestsUsed, r.NRequestsCap)
	return c
}

func statusHLPXYZ(ctx context.Context, env *Env) StatusCheck {
	c := StatusCheck{Name: "hlpxyz"}
	switch {
	case env.Config.HLPXYZ.Disabled:
		c.OK, c.Detail = true, "disabled in config"
		return c
	case env.HLPXYZ == nil && env.pvaultTelemetry == nil:
		c.OK, c.Detail = true, "not used offline"
		return c
	}
	api := env.HLPXYZ
	if api == nil {
		// Testnet: hlp.xyz still serves strategy-vault telemetry.
		start := time.Now()
		if _, err := env.pvaultTelemetry.PVaultStatus(ctx); err != nil {
			c.Detail = AsError(err).Message
			return c
		}
		c.OK, c.LatencyMs, c.Detail = true, since(start), "strategy-vault telemetry"
		return c
	}
	start := time.Now()
	if _, err := api.PVaultStatus(ctx); err != nil {
		c.Detail = AsError(err).Message
		return c
	}
	c.OK, c.LatencyMs, c.Detail = true, since(start), "reachable"
	return c
}

// statusHyperEVM asks the network's HyperEVM chain for eth_chainId through
// the same failover list pvault uses (built-ins plus [hyperevm.<id>]).
func statusHyperEVM(ctx context.Context, env *Env) StatusCheck {
	if _, ok := env.HL.(infoCaller); !ok {
		return StatusCheck{} // offline
	}
	id := evm.ChainMainnet
	if env.Config.Network == domain.Testnet {
		id = evm.ChainTestnet
	}
	c := StatusCheck{Name: fmt.Sprintf("hyperevm:%d", id)}
	chain, ok := evm.MergeChains(evm.DefaultChains(), env.Config.HyperEVMChains())[id]
	if !ok || len(chain.RPC) == 0 {
		c.OK, c.Detail = true, "no RPC configured"
		return c
	}
	cl, err := evm.NewClient(evm.Options{Endpoints: chain.RPC, Timeout: statusTimeout, UserAgent: "hlp-cli/" + version(env.version)})
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	start := time.Now()
	got, err := cl.ChainID(ctx)
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	c.LatencyMs = since(start)
	host := cl.Endpoint()
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	if got != id {
		c.Detail = fmt.Sprintf("%s answered chain %d, want %d", host, got, id)
		return c
	}
	c.OK = true
	c.Detail = host
	if fo := cl.TakeFailovers(); len(fo) > 0 {
		c.Detail += fmt.Sprintf(" (after %d failed endpoint(s))", len(fo))
	}
	return c
}

// statusConfig reports the config file (home shown as ~) and the trading
// state, without touching the keychain or printing any secret.
func statusConfig(_ context.Context, env *Env) StatusCheck {
	c := StatusCheck{Name: "config", OK: true}
	path := redactHome(env.Paths.ConfigFile)
	fi, err := os.Stat(env.Paths.ConfigFile)
	switch {
	case env.Paths.ConfigFile == "":
		c.Detail = "defaults (no config path)"
	case err != nil:
		c.Detail = path + " (not found; using defaults)"
	default:
		c.Detail = path
		if fi.Mode().Perm()&0o022 != 0 {
			c.OK = false
			c.Detail += fmt.Sprintf(" is writable by others (%v): chmod 600 it", fi.Mode().Perm())
		}
	}
	for _, w := range env.Config.Warnings {
		c.Detail += "; " + w
	}
	switch {
	case !env.Config.Trading.Enabled:
		c.Detail += "; trading off"
	case env.Exchange != nil:
		c.Detail += "; trading on, agent key loaded"
	default:
		c.Detail += "; trading on, no agent key"
	}
	return c
}

func redactHome(p string) string {
	if p == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return p
}
