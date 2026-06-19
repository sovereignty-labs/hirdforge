// Package workbench implements the Hirdforge Workbench local runtime: its
// in-memory event log, project/provider state, builder planning and change
// proposals, Lockbox approvals, and the HTTP API that ties them together. The
// cmd/hirdforge-workbench command is a thin entrypoint around this package.
package workbench

import (
	"net/http"
)

const modeName = "workbench"

const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Hirdforge Workbench</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; margin: 2rem; color: #222; }
  h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
  h2 { font-size: 1.1rem; margin-top: 1.5rem; margin-bottom: 0.5rem; }
  p  { color: #555; }
  code { background: #f4f4f4; padding: 0.1rem 0.35rem; border-radius: 3px; }
  ul { list-style: none; padding: 0; }
  li { margin: 0.25rem 0; }
</style>
</head>
<body>
  <h1>Hirdforge Workbench</h1>
  <p>Local-first workbench skeleton. The event stream and builder loop are not wired yet.</p>
  <h2>Endpoints</h2>
  <ul>
    <li><code>GET /api/workbench/events</code></li>
    <li><code>GET /api/workbench/project</code></li>
    <li><code>GET /api/workbench/project/inspect</code></li>
    <li><code>GET /api/workbench/project/inspection</code></li>
    <li><code>GET /api/workbench/provider</code></li>
    <li><code>GET /api/workbench/build/session</code></li>
    <li><code>GET /api/workbench/build/sessions</code></li>
    <li><code>GET /api/workbench/build/prompt</code></li>
    <li><code>GET /api/workbench/build/proposal</code></li>
    <li><code>GET /api/workbench/build/proposals</code></li>
    <li><code>GET /api/workbench/lockbox/request</code></li>
    <li><code>GET /api/workbench/lockbox/requests</code></li>
    <li><code>GET /api/workbench/cortex/task</code></li>
    <li><code>GET /api/workbench/cortex/tasks</code></li>
    <li><code>GET /api/workbench/cortex/lanes</code></li>
    <li><code>GET /api/workbench/cortex/worktrees</code></li>
    <li><code>GET /api/workbench/cortex/lane/proposal</code></li>
    <li><code>GET /api/workbench/cortex/lane/proposals</code></li>
    <li><code>GET /api/workbench/cortex/aggregate</code></li>
    <li><code>GET /api/workbench/cortex/aggregates</code></li>
    <li><code>GET /api/workbench/cortex/aggregate/review</code></li>
    <li><code>GET /api/workbench/cortex/aggregate/reviews</code></li>
    <li><code>GET /api/workbench/validation</code></li>
    <li><code>GET /api/workbench/diff</code></li>
  </ul>
</body>
</html>
`

// Server is the Workbench runtime: it ties the event store and project,
// provider, session, inspection, proposal, and Lockbox state to a small HTTP
// mux. It is the public entry type for the package.
type Server struct {
	store      *eventStore
	project    *projectState
	provider   *providerState
	sessions   *sessionStore
	inspection *inspectionState
	proposals  *proposalStore
	lockbox    *lockboxStore
	cortex     *cortexStore

	cortexLaneProposals *cortexLaneProposalStore
	cortexAggregates    *cortexAggregateStore
	cortexReviews       *cortexReviewStore

	mux *http.ServeMux
}

// New constructs a ready-to-serve Workbench Server with empty in-memory state
// and a single startup event already recorded.
func New() *Server {
	s := newEventStore()
	s.Append(startupType, startupMsg, nil)
	wb := &Server{
		store:      s,
		project:    newProjectState(),
		provider:   newProviderState(),
		sessions:   newSessionStore(),
		inspection: newInspectionState(),
		proposals:  newProposalStore(),
		lockbox:    newLockboxStore(),
		cortex:     newCortexStore(),

		cortexLaneProposals: newCortexLaneProposalStore(),
		cortexAggregates:    newCortexAggregateStore(),
		cortexReviews:       newCortexReviewStore(),
	}
	wb.mux = wb.registerRoutes()
	return wb
}

// Handler returns the HTTP handler that serves the Workbench API.
func (wb *Server) Handler() http.Handler {
	return wb.mux
}

func (wb *Server) registerRoutes() *http.ServeMux {
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

	mux.HandleFunc("/api/workbench/events", wb.handleEvents)

	mux.HandleFunc("/api/workbench/project/open", wb.handleProjectOpen)
	mux.HandleFunc("/api/workbench/project/inspect", wb.handleProjectInspect)
	mux.HandleFunc("/api/workbench/project/inspection", wb.handleProjectInspection)
	mux.HandleFunc("/api/workbench/project", wb.handleProjectGet)

	mux.HandleFunc("/api/workbench/provider/test", wb.handleProviderTest)
	mux.HandleFunc("/api/workbench/provider", wb.handleProviderRoot)

	mux.HandleFunc("/api/workbench/build/start", wb.handleBuildStart)
	mux.HandleFunc("/api/workbench/build/session", wb.handleBuildSession)
	mux.HandleFunc("/api/workbench/build/sessions", wb.handleBuildSessions)
	mux.HandleFunc("/api/workbench/build/prompt", wb.handleBuildPrompt)
	mux.HandleFunc("/api/workbench/build/propose", wb.handleBuildPropose)
	mux.HandleFunc("/api/workbench/build/proposal", wb.handleBuildProposal)
	mux.HandleFunc("/api/workbench/build/proposals", wb.handleBuildProposals)

	mux.HandleFunc("/api/workbench/lockbox/request", wb.handleLockboxRequest)
	mux.HandleFunc("/api/workbench/lockbox/requests", wb.handleLockboxRequests)
	mux.HandleFunc("/api/workbench/lockbox/approve", wb.handleLockboxApprove)
	mux.HandleFunc("/api/workbench/lockbox/reject", wb.handleLockboxReject)

	mux.HandleFunc("/api/workbench/cortex/task", wb.handleCortexTask)
	mux.HandleFunc("/api/workbench/cortex/tasks", wb.handleCortexTasks)
	mux.HandleFunc("/api/workbench/cortex/lanes", wb.handleCortexLanes)
	mux.HandleFunc("/api/workbench/cortex/run", wb.handleCortexRun)
	mux.HandleFunc("/api/workbench/cortex/worktrees", wb.handleCortexWorktrees)
	mux.HandleFunc("/api/workbench/cortex/lane/propose", wb.handleCortexLanePropose)
	mux.HandleFunc("/api/workbench/cortex/lane/proposal", wb.handleCortexLaneProposal)
	mux.HandleFunc("/api/workbench/cortex/lane/proposals", wb.handleCortexLaneProposals)
	mux.HandleFunc("/api/workbench/cortex/aggregate", wb.handleCortexAggregate)
	mux.HandleFunc("/api/workbench/cortex/aggregates", wb.handleCortexAggregates)
	mux.HandleFunc("/api/workbench/cortex/aggregate/lockbox", wb.handleCortexAggregateLockbox)
	mux.HandleFunc("/api/workbench/cortex/aggregate/review", wb.handleCortexAggregateReview)
	mux.HandleFunc("/api/workbench/cortex/aggregate/reviews", wb.handleCortexAggregateReviews)

	mux.HandleFunc("/api/workbench/validation", wb.handleValidation)
	mux.HandleFunc("/api/workbench/diff", wb.handleDiff)

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
