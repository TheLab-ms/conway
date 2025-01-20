package engine

import (
	"net/http"

	"github.com/TheLab-ms/conway/static"
)

// App is a wrapper around the process manager and http router/server concepts defined by this pkg.
// It represents a set of "modules": types that can run workers or handle http routes.
// Just load up modules with .Add() and then run the thing with .ProcMgr.Run().
type App struct {
	ProcMgr
	Router *Router
}

func NewApp(httpAddr string) *App {
	a := &App{Router: NewRouter(http.FileServer(http.FS(static.Assets)))}
	a.Router.Authenticator = a.Router
	a.ProcMgr.Add(Serve(httpAddr, a.Router))
	return a
}

func (a *App) Add(mod any) {
	if m, ok := mod.(routableModule); ok {
		m.AttachRoutes(a.Router)
	}
	if m, ok := mod.(workableModule); ok {
		m.AttachWorkers(&a.ProcMgr)
	}
}

type routableModule interface {
	AttachRoutes(*Router)
}

type workableModule interface {
	AttachWorkers(*ProcMgr)
}
