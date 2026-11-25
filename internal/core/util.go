package core

import (
	"strconv"
	"sync"
)

// parallel runs f(0..n-1) concurrently (bounded to 8 at a time) and waits.
func parallel(n int, f func(i int)) { parallelN(n, 8, f) }

// parallelN runs f(0..n-1) with at most k in flight and waits. Fan-outs
// whose items each fan out again (Accounts: 14 requests each) use a small k
// so one command cannot burst into 429s (PERFORMANCE #1).
func parallelN(n, k int, f func(i int)) {
	sem := make(chan struct{}, max(k, 1))
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			f(i)
		}(i)
	}
	wg.Wait()
}

func itoa(n int) string { return strconv.Itoa(n) }
