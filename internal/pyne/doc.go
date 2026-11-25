// Package pyne drives PYNE (https://github.com/hoox-sh/pyne, PyPI
// "hoox-pyne"), an external Pine Script v5/v6 engine, as an optional
// alternative to hlp's own MIT interpreter in internal/strategy/pine.
//
// License boundary. PYNE is AGPL-3.0. hlp is MIT and stays MIT, so this
// package treats PYNE strictly as a separately installed program:
//
//   - nothing from PYNE is vendored, copied, ported or linked; hlp only
//     starts the `pyne` executable (exec.Command with an argv slice, never a
//     shell) and exchanges files and JSON with it;
//   - the CLI contract used here comes from PYNE's public docs and `--help`
//     output, not from reading its implementation;
//   - PYNE is never required: Detect reports ErrNotInstalled and callers
//     fall back to the builtin engine.
//
// The user installs PYNE themselves (`uv tool install hoox-pyne`) and is
// responsible for complying with its AGPL terms or buying HOOX's commercial
// licence; hlp does neither on their behalf.
//
// CLI contract (hoox-pyne 0.6.x/0.7.x):
//
//	pyne --version                         -> "0.7.0"
//	pyne diagnostics FILE                  -> {"ok","diagnostics":[{code,severity,message,line(1-based),column(0-based)}]}; exit 1 on errors
//	pyne execute FILE --mode interpret --ohlcv BARS.json --inputs INPUTS.json
//	     --ticker T --timeframe TF --sample N [--trades]
//	                                       -> {"ok","bars","plots":{title:{len,last,na_count,sample}},
//	                                           "extras":{plot_meta,events,alerts,meta:{inputs,logs,...}}}
//	                                           or {"ok":false,"error","kind":"parse"|"runtime"}; exit 1
//
// BARS.json is an array of {"time":ms,"open","high","low","close","volume"}.
// Inputs are keyed by input title. Plots only carry the trailing `sample`
// values (max 64), so strategy results (closed trades, per-bar equity and
// position) are recovered by appending a few log.info() lines to the script
// (see instrument.go) and parsing extras.meta.logs.
package pyne
