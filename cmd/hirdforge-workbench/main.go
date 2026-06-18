package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	defaultHost = "127.0.0.1"
	defaultPort = "7777"
	modeName    = "workbench"
	startupType = "workbench.started"
	startupMsg  = "Hirdforge Workbench started"
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

// WorkbenchEvent is a single entry in the in-memory Cortex-lite event log.
type WorkbenchEvent struct {
	ID      string          `json:"id"`
	TS      time.Time       `json:"ts"`
	Type    string          `json:"type"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// eventStore is a small thread-safe append-only log used by the workbench.
type eventStore struct {
	mu     sync.Mutex
	nextID int64
	events []WorkbenchEvent
}

func newEventStore() *eventStore {
	return &eventStore{}
}

// Append records a new event and returns the stored entry, including the
// assigned id and timestamp.
func (s *eventStore) Append(evType, message string, data json.RawMessage) WorkbenchEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	ev := WorkbenchEvent{
		ID:      strconv.FormatInt(s.nextID, 10),
		TS:      time.Now().UTC(),
		Type:    evType,
		Message: message,
		Data:    data,
	}
	s.events = append(s.events, ev)
	return ev
}

// List returns a snapshot copy of the current event log.
func (s *eventStore) List() []WorkbenchEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]WorkbenchEvent, len(s.events))
	copy(out, s.events)
	return out
}

// workbench ties the event store to a small HTTP mux.
type workbench struct {
	store *eventStore
	mux   *http.ServeMux
}

func newWorkbench() *workbench {
	s := newEventStore()
	s.Append(startupType, startupMsg, nil)
	wb := &workbench{store: s}
	wb.mux = wb.registerRoutes()
	return wb
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (wb *workbench) registerRoutes() *http.ServeMux {
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
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, wb.store.List())
		case http.MethodPost:
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
				return
			}
			var in struct {
				Type    string          `json:"type"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(body, &in); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if in.Type == "" {
				http.Error(w, "type is required", http.StatusBadRequest)
				return
			}
			ev := wb.store.Append(in.Type, in.Message, in.Data)
			writeJSON(w, http.StatusCreated, ev)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
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
	wb := newWorkbench()
	server := &http.Server{
		Addr:              addr,
		Handler:           wb.mux,
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
