package agent

import (
	"context"
	"sync"
	"time"
)

// periodic caches the result of an expensive probe (update search, event log) and refreshes
// it every `every`; after an error it tries again after `retry`. With background refreshes a
// slow probe never delays a report: Get returns the previous value meanwhile.
type periodic[T any] struct {
	every, retry, timeout time.Duration
	fn                    func(context.Context) (T, error)
	now                   func() time.Time

	mu      sync.Mutex
	val     T
	err     error
	have    bool // val/err hold a result
	good    bool // val came from a successful run
	next    time.Time
	running bool
	done    chan struct{} // closed when the running refresh finishes
}

func newPeriodic[T any](every, retry, timeout time.Duration, fn func(context.Context) (T, error)) *periodic[T] {
	return &periodic[T]{every: every, retry: retry, timeout: timeout, fn: fn, now: time.Now}
}

// Get returns the cached result and whether there is one. When the result is due, a refresh
// starts; with wait the call blocks until it is finished (or ctx ends).
func (p *periodic[T]) Get(ctx context.Context, wait bool) (T, error, bool) {
	p.mu.Lock()
	if !p.running && !p.now().Before(p.next) {
		p.running = true
		p.done = make(chan struct{})
		go p.refresh()
	}
	done := p.done
	running := p.running
	p.mu.Unlock()

	if wait && running {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.val, p.err, p.have
}

func (p *periodic[T]) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	v, err := p.fn(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case err == nil:
		p.val, p.err, p.good, p.next = v, nil, true, p.now().Add(p.every)
	case p.good:
		// Keep the last good value (a little stale is better than an error) and retry sooner.
		p.next = p.now().Add(p.retry)
	default:
		p.val, p.err, p.next = v, err, p.now().Add(p.retry)
	}
	p.have = true
	p.running = false
	close(p.done)
}
