// Package thesis turns a plain investment thesis into a target-weight
// strategy: "long SPY, short memecoins, rebalance weekly, 2x".
//
// YAML (examples/thesis/long-spy-short-memes.yaml):
//
//	name: Long SPY / Short memes
//	interval: 1d
//	rebalance: 1w
//	leverage: 2            # gross; legs are scaled so sum |w| = leverage
//	long:
//	  - coin: xyz:SP500    # HIP-3 (TradeXYZ); "SPY" resolves here via the registry
//	    weight: 1
//	short:
//	  - basket: memes      # built-in basket, equal-weighted
//	baskets:               # optional custom baskets
//	  memes: [DOGE, FARTCOIN, TRUMP, PUMP, kPEPE, WIF]
//
// Note: a thesis with HIP-3 legs cannot run in a legacy HyperCore vault
// (validator perps only); Validate warns and points at HyperEVM vaults.
package thesis
