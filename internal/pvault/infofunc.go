package pvault

// InfoFuncOf returns the raw /info poster behind a CoreInfo built by this
// package (InfoCoreInfo or *HTTPCoreInfo), so other read-only packages
// (hyperevm/safe) share its pacing or the session's rate limiter. It
// returns nil for any other implementation.
func InfoFuncOf(c CoreInfo) InfoFunc {
	switch v := c.(type) {
	case InfoCoreInfo:
		return v.Info
	case *InfoCoreInfo:
		return v.Info
	case *HTTPCoreInfo:
		return v.Info
	}
	return nil
}
