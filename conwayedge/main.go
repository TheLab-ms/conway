package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	lan := flag.String("lan", ":8080", "LAN controller/config listen address")
	tunnel := flag.String("tunnel", "127.0.0.1:8081", "cloudflared origin listen address")
	data := flag.String("data", "data", "persistent data directory (one process only)")
	flag.Parse()
	token, password := os.Getenv("CONWAYEDGE_TOKEN"), os.Getenv("CONWAYEDGE_ADMIN_PASSWORD")
	if token == "" || password == "" {
		return fmt.Errorf("CONWAYEDGE_TOKEN and CONWAYEDGE_ADMIN_PASSWORD are required")
	}
	e, err := openEdge(*data)
	if err != nil {
		return err
	}
	defer e.printers.close()
	e.signingKey, err = loadSigningSeed(os.Getenv("CONWAYEDGE_SIGNING_SEED"))
	if err != nil {
		return err
	}
	lanHandler, tunnelHandler := e.routes(token, password)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	local, err := net.Listen("tcp", *lan)
	if err != nil {
		return err
	}
	defer local.Close()
	cloud, err := net.Listen("tcp", *tunnel)
	if err != nil {
		return err
	}
	defer cloud.Close()
	errors := make(chan error, 2)
	for i, listener := range []net.Listener{local, cloud} {
		server := &http.Server{
			Handler:           []http.Handler{lanHandler, tunnelHandler}[i],
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
			MaxHeaderBytes: 16 << 10,
			BaseContext:    func(net.Listener) context.Context { return ctx },
		}
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				_ = server.Close()
			}
		}()
		go func() { errors <- server.Serve(listener) }()
		log.Printf("listening on %s", listener.Addr())
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errors:
		stop()
		return err
	}
}

func secretEqual(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

func (e *edge) routes(token, password string) (http.Handler, http.Handler) {
	lan, tunnel := http.NewServeMux(), http.NewServeMux()
	lan.HandleFunc("POST /api/fobs", e.fobs)
	lan.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || !secretEqual(pass, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="conwayedge"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		e.configure(w, r)
	})
	tunnel.HandleFunc("POST /api/goal", e.goal)
	tunnel.HandleFunc("POST /api/goal/versioned", e.versionedGoal)
	tunnel.HandleFunc("GET /api/swipes", e.getSwipes)
	tunnel.HandleFunc("POST /api/swipes/ack", e.ackSwipes)
	tunnel.HandleFunc("GET /api/printers", e.printers.status)
	tunnel.HandleFunc("GET /machines/stream/{serial}", e.printers.stream)
	return lan, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !secretEqual(r.Header.Get("Authorization"), "Bearer "+token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		tunnel.ServeHTTP(w, r)
	})
}
