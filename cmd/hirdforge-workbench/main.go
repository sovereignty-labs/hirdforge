// Command hirdforge-workbench is a thin entrypoint around the
// internal/workbench runtime. It parses flags, resolves the listen address,
// constructs the Workbench server, runs the HTTP server, and logs
// startup/shutdown. All product behavior lives in internal/workbench.
package main

import (
	"context"
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

	"git.hirdforge.com/kit/hirdforge/internal/workbench"
)

const (
	defaultHost = "127.0.0.1"
	defaultPort = "7777"
)

// resolveAddr returns the listen address from HIRDFORGE_WORKBENCH_HOST/PORT,
// falling back to the local defaults.
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
		Handler:           workbench.New().Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("hirdforge-workbench: listening on http://%s (mode=workbench)", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		log.Printf("hirdforge-workbench: shutting down")
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
