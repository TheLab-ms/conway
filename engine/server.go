package engine

import (
	"context"
	"log/slog"
	"net/http"
)

// Serve wires up the stdlib http server to the engine.
func Serve(addr string, handler http.Handler) Proc {
	return func(ctx context.Context) error {
		svr := &http.Server{
			Handler: handler,
			Addr:    addr,
		}
		go func() {
			<-ctx.Done()
			slog.Warn("gracefully shutting down http server...")
			svr.Shutdown(context.Background())
		}()
		if err := svr.ListenAndServe(); err != nil {
			return err
		}
		slog.Info("the http server has shut down")
		return nil
	}
}
