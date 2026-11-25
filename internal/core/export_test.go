package core

// SetLiveBuild overrides the compile-time live gate for one test and returns
// the restore func. Only tests can reach it; they pair it with a fake
// exchange.Client so nothing is ever sent.
func SetLiveBuild(live bool) (restore func()) {
	prev := liveBuild
	liveBuild = func() bool { return live }
	return func() { liveBuild = prev }
}
