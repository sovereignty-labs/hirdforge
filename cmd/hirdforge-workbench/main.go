package main

import (
	"context"
	"encoding/json"
	"errors"
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

const (
	defaultHost = "127.0.0.1"
	defaultPort = "7777"
	modeName    = "workbench"
)

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Hirdforge Workbench</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; margin: 2rem; color: #222; }
  h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
  p  { color: #555; }
  code { background: #f4f4f4; padding: 0.1rem 0.35rem; border-radius: 3px; }
</style>
</head>
<body>
  <h1>Hirdforge Workbench</h1>
  <p>Local-first workbench skeleton. The event stream and builder loop are not wired yet.</p>
  <p>Try <code>GET /health</code> and <code>GET /api/workbench/events</code>.</p>
</body>
</html>
`

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func newWorkbenchMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
			"mode":   modeName,
		})
	})

	mux.HandleFunc("/api/workbench/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, []struct{}{})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(indexHTML))
	})

	return mux
}

func resolveAddr() string {
	host := os.Getenv("HIRDFORGE_WORKBENCH_HOST")
	if host == "" {
		host = defaultHost
	}
	port := os.Getenv("HIRDFORGE_WORKBENCH_PORT")
	if port == "" {
		port = defaultPort
	}
	return net.JoinHostPort(host, port)
}

func run(ctx context.Context, addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           newWorkbenchMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("hirdforge-workbench: listening on http://%s (mode=%s)", addr, modeName)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func main() {
	addrFlag := flag.String("addr", "", "listen address (host:port); overrides HIRDFORGE_WORKBENCH_HOST/PORT")
	flag.Parse()

	addr := *addrFlag
	if addr == "" {
		addr = resolveAddr()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, addr); err != nil {
		fmt.Fprintf(os.Stderr, "hirdforge-workbench: %v\n", err)
		os.Exit(1)
	}
}
