// Package exchange builds, signs and (only when every gate is open) posts
// Hyperliquid /exchange actions.
//
// HARD RULES for this build:
//   - Tests sign ONLY with the SDK's public test key
//     0x0123456789012345678901234567890123456789012345678901234567890123
//     and must reproduce the SDK's published signing vectors exactly.
//   - Nothing in CI or development posts to /exchange. Post refuses unless
//     Gate.Live is true; Gate.Live is only set by core after config
//     Trading.Enabled, an agent key, risk caps, and confirmation all pass.
//   - Phase-2 gate: the owner runs order/cancel/modify/updateLeverage/
//     vaultTransfer once on testnet with a throwaway key before any mainnet
//     path is enabled.
//
// msgpack rules: structs with fields in Python-SDK order (never maps),
// no int64 fields (use int/uint64 or UseCompactInts), optional keys omitted
// entirely (omitempty), decimal strings with trailing zeros stripped,
// lowercase addresses. Crypto: decred secp256k1 + x/crypto/sha3 (no cgo, no
// go-ethereum).
package exchange
