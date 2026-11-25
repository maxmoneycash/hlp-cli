package evm

import "strings"

// Chain is one HyperEVM network as configured in hlp.toml:
//
//	[hyperevm.998]
//	name = "HyperEVM Testnet"
//	rpc  = ["https://rpc.hyperliquid-testnet.xyz/evm", "https://hyperliquid-testnet.drpc.org"]
//	lens = "0xBe615AB094039EeaE9dbfa5512b5EB6ADC248Be2"
//	usdc = "0x2B3370eE501B4a559b57D449569354196457D8Ab"
//	info = "https://api.hyperliquid-testnet.xyz/info"
//
// An empty Lens means "read the vault's own views" (mainnet today).
//
// A configured rpc list is tried before the built-in endpoints (which stay
// as fallbacks); set rpc_only = true to use the configured list alone.
type Chain struct {
	ID   uint64   `toml:"-" json:"chainId"`
	Name string   `toml:"name" json:"name"`
	RPC  []string `toml:"rpc" json:"rpc"`
	// RPCOnly makes RPC replace the built-in endpoints instead of being
	// tried before them.
	RPCOnly bool   `toml:"rpc_only,omitempty" json:"rpcOnly,omitempty"`
	Lens    string `toml:"lens" json:"lens,omitempty"`
	USDC    string `toml:"usdc" json:"usdc,omitempty"`
	// Info is the HyperCore /info URL for the same network (testnet Core
	// for chain 998). Used for positions, agents and lens-less NAV.
	Info string `toml:"info" json:"info,omitempty"`
}

// Testnet reports chain 998.
func (c Chain) Testnet() bool { return c.ID == ChainTestnet }

// Known chain ids.
const (
	ChainTestnet uint64 = 998
	ChainMainnet uint64 = 999
)

// DefaultChains are the built-in chain configs. The official testnet RPC is listed first but fails TLS from some
// networks, so drpc follows as the fallback.
func DefaultChains() map[uint64]Chain {
	return map[uint64]Chain{
		ChainTestnet: {
			ID:   ChainTestnet,
			Name: "HyperEVM Testnet",
			RPC:  []string{"https://rpc.hyperliquid-testnet.xyz/evm", "https://hyperliquid-testnet.drpc.org"},
			Lens: "0xBe615AB094039EeaE9dbfa5512b5EB6ADC248Be2",
			USDC: "0x2B3370eE501B4a559b57D449569354196457D8Ab",
			Info: "https://api.hyperliquid-testnet.xyz/info",
		},
		ChainMainnet: {
			ID:   ChainMainnet,
			Name: "HyperEVM",
			RPC:  []string{"https://rpc.hyperliquid.xyz/evm"},
			USDC: "0xb88339CB7199b77E23DB6E890353E22632Ba630f",
			Info: "https://api.hyperliquid.xyz/info",
		},
	}
}

// MergeChains overlays user config on the defaults: non-empty fields win,
// except RPC, which is prepended to the base list (duplicates dropped) so
// the built-in endpoints remain as fallbacks. An overlay with RPCOnly
// replaces the base list instead.
func MergeChains(base, over map[uint64]Chain) map[uint64]Chain {
	out := make(map[uint64]Chain, len(base)+len(over))
	for id, c := range base {
		out[id] = c
	}
	for id, c := range over {
		b := out[id]
		b.ID = id
		if c.Name != "" {
			b.Name = c.Name
		}
		switch {
		case len(c.RPC) > 0 && c.RPCOnly:
			b.RPC = DedupeRPC(c.RPC)
		case len(c.RPC) > 0:
			b.RPC = PrependRPC(b.RPC, c.RPC...)
		}
		b.RPCOnly = b.RPCOnly || c.RPCOnly
		if c.Lens != "" {
			b.Lens = c.Lens
		}
		if c.USDC != "" {
			b.USDC = c.USDC
		}
		if c.Info != "" {
			b.Info = c.Info
		}
		out[id] = b
	}
	return out
}

// PrependRPC returns first followed by list, without blanks or duplicates
// (compared case-insensitively, ignoring a trailing slash). The result is
// a new slice.
func PrependRPC(list []string, first ...string) []string {
	return DedupeRPC(append(append([]string(nil), first...), list...))
}

// DedupeRPC trims, drops blanks and drops repeated endpoints, keeping the
// first occurrence.
func DedupeRPC(urls []string) []string {
	out := make([]string, 0, len(urls))
	seen := map[string]bool{}
	for _, u := range urls {
		u = strings.TrimSpace(u)
		k := strings.ToLower(strings.TrimRight(u, "/"))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, u)
	}
	return out
}

// SplitRPCList parses a comma- or whitespace-separated endpoint list (the
// HLP_HYPEREVM_RPC variable, a repeated --rpc value).
func SplitRPCList(s ...string) []string {
	var out []string
	for _, v := range s {
		out = append(out, strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })...)
	}
	return DedupeRPC(out)
}
