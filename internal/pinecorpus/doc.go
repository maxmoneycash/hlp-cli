// Package pinecorpus reads the PineForge validation corpus
// (github.com/pineforge-4pass/pineforge-corpus) so hlp's Pine engines can
// be checked against TradingView's own trade lists.
//
// The corpus is not vendored. Its Pine sources, generated C++ and engine
// tapes are Apache-2.0, but its TradingView "List of Trades" exports
// (tv_trades.csv) are not PineForge-licensed works and their public
// redistribution under TradingView's Terms of Service is unresolved, so hlp
// never commits them. Tests that need the corpus read it from the directory
// named by $HLP_PINECORPUS_DIR, or with HLP_CORPUS=1 fetch the pinned
// revision into the user cache (Root, Fetch), and skip otherwise.
//
// Besides reading the corpus, the package grades any Pine engine against
// TradingView: an Engine adapter returns trades for a Job, GradeTrades and
// Score compare them with the probe's tape, and Markdown renders the
// scoreboard (see internal/pinecorpus/scoreboard and docs/PINEFORGE.md).
//
// This package contains no code from the corpus or from any PineForge
// repository. It only parses the corpus's file formats: TradingView's
// trade-list CSV columns and the probe directory layout.
package pinecorpus
