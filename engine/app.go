package engine

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/TheLab-ms/conway/engine/config"
)

// App is a wrapper around the process manager and http router/server concepts defined by this pkg.
// It represents a set of "modules": types that can run workers or handle http routes.
// Just load up modules with .Add() and then run the thing with .ProcMgr.Run().
type App struct {
	ProcMgr
	Router         *Router
	configRegistry *config.Registry
	configStore    *config.Store
}

func NewApp(httpAddr string, router *Router, db *sql.DB) *App {
	registry := config.NewRegistry(db)
	a := &App{
		Router:         router,
		configRegistry: registry,
		configStore:    config.NewStore(db, registry),
	}
	a.ProcMgr.Add(router.Serve(httpAddr))
	return a
}

func (a *App) Configs() *config.Registry { return a.configRegistry }

// ConfigStore returns the shared config store for typed config loading.
func (a *App) ConfigStore() *config.Store { return a.configStore }

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

	// Auto-register modules that provide a ConfigSpec
	type configurableModule interface {
		ConfigSpec() config.Spec
	}
	if m, ok := mod.(configurableModule); ok {
		a.configRegistry.MustRegister(m.ConfigSpec())
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
