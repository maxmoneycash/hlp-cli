// Package config loads ~/.config/hlp/config.toml, resolves XDG paths, and
// owns secrets (keys) via environment variables or the OS keychain.
//
// Hard rules:
//   - Secrets are never written to config.toml, never logged, never put on argv.
//   - Every type that can hold a secret implements fmt.Formatter/String with
//     redaction (see Secret). Hold a Secret in an exported field or a local:
//     fmt cannot call methods on unexported struct fields, so a Secret inside
//     an unexported field of a struct printed with %v would show its value.
//   - Trading is OFF unless Trading.Enabled is true AND an agent key exists.
//
// Loading is strict: unknown keys are errors that name the key, its line and
// the closest known key ("risk.max_levrage: unknown key (did you mean
// "risk.max_leverage"?)"). Keys the file omits keep Defaults(). Env overrides
// (HLP_NETWORK, HLP_THEME, HLP_ACCOUNT) apply after the file, then the result
// is validated. Validation also rejects things that look like private keys or
// API keys pasted into address or env-var fields.
//
// File layout:
//
//	config.go   Config, Defaults, Paths, Load, Parse, ApplyEnv, Validate, Save
//	secrets.go  Secret (redacting), Secrets (env + OS keychain), EnvFor
//	risk.go     RiskConfig.Validate, AllowsCoin, CheckOrder (used by core's RiskGuard)
package config
