package engine

import (
	"context"
	"database/sql"
	"embed"
	"log/slog"
	"net/http"
	"time"
)

//go:embed assets/*
var assetFS embed.FS

type Authenticator interface {
	WithAuthn(http.HandlerFunc) http.HandlerFunc
	WithLeadership(http.HandlerFunc) http.HandlerFunc
}

type noopAuthenticator struct{}

func (noopAuthenticator) WithAuthn(fn http.HandlerFunc) http.HandlerFunc      { return fn }
func (noopAuthenticator) WithLeadership(fn http.HandlerFunc) http.HandlerFunc { return fn }

type Router struct {
	router *http.ServeMux

	// Authenticator can be used to pass an authenticator implementation to other handlers.
	Authenticator
}

func NewRouter() *Router {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(assetFS)))
	return &Router{router: mux, Authenticator: noopAuthenticator{}}
}

// Serve wires up the stdlib http server to the engine.
func (r *Router) Serve(addr string) Proc {
	return func(ctx context.Context) error {
		svr := &http.Server{Handler: r, Addr: addr}
		go func() {
			<-ctx.Done()
			slog.Warn("gracefully shutting down http server...")
			svr.Shutdown(context.Background())
		}()
		if err := svr.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		slog.Info("the http server has shut down")
		return nil
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, rr *http.Request) { r.router.ServeHTTP(w, rr) }

func (r *Router) HandleFunc(route string, fn http.HandlerFunc) {
	r.router.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		ww := &responseWrapper{ResponseWriter: w, status: 200}
		fn(ww, r)
		slog.Info("http request", "url", r.URL.Path, "method", r.Method, "userAgent", r.UserAgent(), "latencyMS", time.Since(start).Milliseconds(), "status", ww.status)
	})
}

// SystemError logs the given message+args while returning a styled 500 error page.
func SystemError(w http.ResponseWriter, msg string, args ...any) {
	slog.Error(msg, args...)
	ClientError(w, "Uh oh", "Internal error - please try again later", 500)
}

// ClientError renders a styled HTML error page with the given title, message, and status code.
func ClientError(w http.ResponseWriter, title, message string, code int) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(code)
	renderError(title, message).Render(context.Background(), w)
}

// HandleError returns true if err is non-nil, logging the error and sending
// a 500 response. This allows cleaner error handling in handlers:
//
//	if engine.HandleError(w, err) {
//	    return
//	}
func HandleError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	SystemError(w, err.Error())
	return true
}

type FormHandler struct {
	Query  string
	Fields []string
}

func (f *FormHandler) Handler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		args := []any{
			sql.Named("route_id", r.PathValue("id")),
		}
		for _, field := range f.Fields {
			args = append(args, sql.Named(field, r.FormValue(field)))
		}

		_, err := db.ExecContext(r.Context(), f.Query, args...)
		if err != nil {
			SystemError(w, err.Error())
			return
		}

		http.Redirect(w, r, r.Referer(), http.StatusSeeOther)
	}
}

type responseWrapper struct {
	http.ResponseWriter
	status int
}

func (w *responseWrapper) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Flush implements http.Flusher to support streaming responses (e.g., MJPEG).
func (w *responseWrapper) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
