package core

import "context"

type seedKey struct{}

// WithSeed hands a stream a result its caller fetched moments ago, so the
// stream starts from it instead of repeating the REST snapshot (PERFORMANCE
// #4). Callers seed only fresh results (the TUI: Meta.AsOf under 60 s).
func WithSeed(ctx context.Context, r *Result) context.Context {
	return context.WithValue(ctx, seedKey{}, r)
}

// SeedFrom returns the seed set by WithSeed, if any.
func SeedFrom(ctx context.Context) (*Result, bool) {
	r, ok := ctx.Value(seedKey{}).(*Result)
	return r, ok && r != nil
}
