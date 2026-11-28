// Work a dispatch loop starts without waiting for it: Direct jobs and resumed chains.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
)

// defaultDirectConcurrency is how many Direct jobs and resumed chains run at once when
// ExecutorConfig.DirectConcurrency is unset.
const defaultDirectConcurrency = 8

// slots is shared by every loop of one ExecutorConfig, so the bound is the process's.
func (c *ExecutorConfig) slots() chan struct{} {
	c.slotsOnce.Do(func() {
		n := c.DirectConcurrency
		if n <= 0 {
			n = defaultDirectConcurrency
		}
		c.slotsCh = make(chan struct{}, n)
	})
	return c.slotsCh
}

// background is one loop's share of that work. The loop waits for it before it returns, so shutdown
// does not cut a job off before its result is recorded.
type background struct {
	config *ExecutorConfig
	wg     sync.WaitGroup
}

func newBackground(config *ExecutorConfig) *background { return &background{config: config} }

// run starts fn and returns at once, unless every slot is busy: then the caller waits for one, which
// is what keeps a burst from starting unbounded work. false: ctx ended first and fn never ran.
func (b *background) run(ctx context.Context, fn func()) bool {
	slots := b.config.slots()
	select {
	case slots <- struct{}{}:
	default:
		slog.Warn("every direct-job slot is busy, waiting for one", "slots", cap(slots))
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return false
		}
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() { <-slots }()
		fn()
	}()
	return true
}

func (b *background) wait() { b.wg.Wait() }
