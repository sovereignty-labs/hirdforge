// Package workbench implements the Hirdforge Workbench local runtime: its
// in-memory event log, project/provider state, builder planning and change
// proposals, Lockbox approvals, and the HTTP API that ties them together. The
// cmd/hirdforge-workbench command is a thin entrypoint around this package.
package workbench

import (
	_ "embed"
	"net/http"
)

const modeName = "workbench"

// indexHTML is the embedded Lane Console operator UI served at "/". It is a
// single self-contained page (HTML/CSS/vanilla JS) that calls the live
// Workbench APIs; see internal/workbench/ui/index.html.
//
//go:embed ui/index.html
var indexHTML string

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
	cortexApplies       *cortexApplyStore
	cortexApplyPreviews *cortexApplyPreviewStore
	cortexValidations   *cortexValidationStore

	architect         *architectSessionStore
	laneConversations *laneConversationStore

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
		cortexApplies:       newCortexApplyStore(),
		cortexApplyPreviews: newCortexApplyPreviewStore(),
		cortexValidations:   newCortexValidationStore(),

		architect:         newArchitectSessionStore(),
		laneConversations: newLaneConversationStore(),
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
	mux.HandleFunc("/api/workbench/cortex/apply", wb.handleCortexApply)
	mux.HandleFunc("/api/workbench/cortex/applies", wb.handleCortexApplies)
	mux.HandleFunc("/api/workbench/cortex/apply/preview", wb.handleCortexApplyPreview)
	mux.HandleFunc("/api/workbench/cortex/apply/previews", wb.handleCortexApplyPreviews)
	mux.HandleFunc("/api/workbench/cortex/apply/validate", wb.handleCortexApplyValidate)
	mux.HandleFunc("/api/workbench/cortex/apply/validation", wb.handleCortexApplyValidation)
	mux.HandleFunc("/api/workbench/cortex/apply/validations", wb.handleCortexApplyValidations)

	mux.HandleFunc("/api/workbench/architect/session", wb.handleArchitectSession)
	mux.HandleFunc("/api/workbench/architect/sessions", wb.handleArchitectSessions)
	mux.HandleFunc("/api/workbench/architect/message", wb.handleArchitectMessage)
	mux.HandleFunc("/api/workbench/architect/accept", wb.handleArchitectAccept)
	mux.HandleFunc("/api/workbench/architect/cortex-task", wb.handleArchitectCortexTask)

	mux.HandleFunc("/api/workbench/lane-conversation", wb.handleLaneConversation)
	mux.HandleFunc("/api/workbench/lane-conversations", wb.handleLaneConversations)
	mux.HandleFunc("/api/workbench/lane-conversation/message", wb.handleLaneConversationMessage)
	mux.HandleFunc("/api/workbench/lane-conversation/close", wb.handleLaneConversationClose)

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
