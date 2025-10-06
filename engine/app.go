package engine

import (
	"context"
	"fmt"
	"sync"
)

// App is a wrapper around the process manager and http router/server concepts defined by this pkg.
// It represents a set of "modules": types that can run workers or handle http routes.
// Just load up modules with .Add() and then run the thing with .ProcMgr.Run().
type App struct {
	ProcMgr
	Router *Router
}

func NewApp(httpAddr string, router *Router) *App {
	a := &App{Router: router}
	a.ProcMgr.Add(router.Serve(httpAddr))
	return a
}

func (p *ProcMgr) Run(ctx context.Context) { p.run(ctx) }

func (a *App) Add(mod any) {
	type routableModule interface {
		AttachRoutes(*Router)
	}
	if m, ok := mod.(routableModule); ok {
		m.AttachRoutes(a.Router)
	}

	type workableModule interface {
		AttachWorkers(*ProcMgr)
	}
	if m, ok := mod.(workableModule); ok {
		m.AttachWorkers(&a.ProcMgr)
	}
}

type Proc func(context.Context) error

// ProcMgr is like a fancy implementation of sync.WaitGroup.
type ProcMgr struct {
	procs []Proc
}

func (p *ProcMgr) Add(proc Proc) { p.procs = append(p.procs, proc) }

func (p *ProcMgr) run(ctx context.Context) {
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
