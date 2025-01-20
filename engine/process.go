package engine

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type Proc func(context.Context) error

// ProcMgr is like a fancy implementation of sync.WaitGroup.
type ProcMgr struct {
	procs []Proc
}

func (p *ProcMgr) Add(proc Proc) { p.procs = append(p.procs, proc) }

func (p *ProcMgr) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, proc := range p.procs {
		wg.Add(1)
		go func(proc Proc) {
			defer wg.Done()
			err := proc(ctx)
			if err == nil && ctx.Err() == nil {
				panic("a proc returned unexpectedly!")
			}
			if err != nil && ctx.Err() == nil {
				panic(fmt.Sprintf("proc returned an error: %s", err))
			}
		}(proc)
	}
	wg.Wait()
}

// Poll is a Proc that polls a given function regularly.
// If the function returns true, it will be called again immediately.
// This is useful for polling a queue for new items.
func Poll(interval time.Duration, fn func(context.Context) bool) Proc {
	return func(ctx context.Context) error {
		jitter := time.Duration(interval)
		ticker := time.NewTicker(jitter)
		defer ticker.Stop()
		for {
			if fn(ctx) {
				continue // take possible next item immediately
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
			ticker.Reset(time.Duration(float64(interval) * (0.9 + 0.2*rand.Float64())))
		}
	}
}
