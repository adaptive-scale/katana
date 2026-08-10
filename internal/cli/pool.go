package cli

import (
	"context"
	"sync"
)

// runPool runs do over items with the given number of workers and hands each
// result to collect.
//
// Both generation and discovery wait on an agent CLI rather than on this
// machine, so both run several at once. collect is called on the calling
// goroutine, one result at a time, which is what lets a caller update shared
// state — a tracker, a counter — without locking it.
//
// Cancelling ctx stops the queue; workers drain what is already in flight, so
// an interrupt leaves the work that had not started untouched rather than
// killing an agent mid-write.
func runPool[T, R any](ctx context.Context, workers int, items []T, do func(context.Context, T) R, collect func(R)) {
	queue := make(chan T)
	// Buffered for every item, so a worker is never blocked on the collector and
	// an interrupt is never delayed by an unread result.
	results := make(chan R, len(items))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range queue {
				if ctx.Err() != nil {
					continue // the run is stopping; drain the queue
				}
				results <- do(ctx, it)
			}
		}()
	}
	go func() {
		defer close(queue)
		for _, it := range items {
			select {
			case queue <- it:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		collect(res)
	}
}
