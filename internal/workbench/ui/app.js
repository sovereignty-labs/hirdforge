"use strict";

// ===========================================================================
// Hirdforge Workbench — Lane Console operator UI.
//
// Layers, kept deliberately small (no framework, no build step):
//   request()  low-level fetch wrapper that never throws
//   api        named API calls (one per backend endpoint the loop uses)
//   state      the single app state object
//   render*()  DOM rendering from state
//   action     handlers wiring buttons -> api -> state -> render
// ===========================================================================

// ---- low-level request ----------------------------------------------------
var inflight = 0;
function setLoading() {
  state.loading = inflight > 0;
  var el = $("loading");
  if (el) el.classList.toggle("hidden", !state.loading);
}

// request wraps fetch and never throws: it returns {ok, status, data, error}.
// The backend returns plain-text errors for most handlers and JSON for some, so
// we surface whichever is present.
async function request(method, path, body) {
  var opts = { method: method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  inflight++; setLoading();
  var res, text;
  try {
    res = await fetch(path, opts);
    text = await res.text();
  } catch (e) {
    return { ok: false, status: 0, data: null, error: "network error: " + e.message };
  } finally {
    inflight--; setLoading();
  }
  var data = null;
  if (text) { try { data = JSON.parse(text); } catch (e) { data = text; } }
  var error = "";
  if (!res.ok) {
    error = (data && data.error) || (typeof data === "string" ? data.trim() : "") || res.statusText;
  }
  return { ok: res.ok, status: res.status, data: data, error: error };
}

// ---- api: one boring method per endpoint the operator loop uses ------------
var api = {
  getEvents: function () { return request("GET", "/api/workbench/events"); },

  getProject: function () { return request("GET", "/api/workbench/project"); },
  openProject: function (path) { return request("POST", "/api/workbench/project/open", { path: path }); },

  getProvider: function () { return request("GET", "/api/workbench/provider"); },
  saveProvider: function (config) { return request("POST", "/api/workbench/provider", config); },
  testProvider: function () { return request("POST", "/api/workbench/provider/test"); },

  getArchitectSession: function () { return request("GET", "/api/workbench/architect/session"); },
  getArchitectSessions: function () { return request("GET", "/api/workbench/architect/sessions"); },
  createArchitectSession: function (goal) { return request("POST", "/api/workbench/architect/session", { goal: goal }); },
  sendArchitectMessage: function (sessionId, message) {
    return request("POST", "/api/workbench/architect/message", { session_id: sessionId, message: message });
  },
  acceptArchitectSpec: function (sessionId) {
    return request("POST", "/api/workbench/architect/accept", { session_id: sessionId });
  },
  createCortexTask: function (sessionId) {
    return request("POST", "/api/workbench/architect/cortex-task", { session_id: sessionId });
  },

  getCortexTask: function () { return request("GET", "/api/workbench/cortex/task"); },
  getLanes: function () { return request("GET", "/api/workbench/cortex/lanes"); },
  getLaneProposals: function () { return request("GET", "/api/workbench/cortex/lane/proposals"); },
  generateLaneProposal: function (taskId, laneId) {
    return request("POST", "/api/workbench/cortex/lane/propose", { task_id: taskId, lane_id: laneId });
  },

  getAggregate: function () { return request("GET", "/api/workbench/cortex/aggregate"); },
  createAggregate: function (taskId) { return request("POST", "/api/workbench/cortex/aggregate", { task_id: taskId }); },
  getReview: function () { return request("GET", "/api/workbench/cortex/aggregate/review"); },
  createReview: function (aggregateId, laneId) {
    return request("POST", "/api/workbench/cortex/aggregate/review", { aggregate_id: aggregateId, lane_id: laneId });
  },

  getLockboxRequests: function () { return request("GET", "/api/workbench/lockbox/requests"); },
  requestAggregateLockbox: function (aggregateId) { return request("POST", "/api/workbench/cortex/aggregate/lockbox", { id: aggregateId }); },
  approveLockbox: function (id, reason) { return request("POST", "/api/workbench/lockbox/approve", { id: id, reason: reason || "" }); },
  rejectLockbox: function (id, reason) { return request("POST", "/api/workbench/lockbox/reject", { id: id, reason: reason || "" }); },
  getApplyPreview: function () { return request("GET", "/api/workbench/cortex/apply/preview"); },
  createApplyPreview: function (lockboxRequestId) {
    return request("POST", "/api/workbench/cortex/apply/preview", { lockbox_request_id: lockboxRequestId });
  },

  getApply: function () { return request("GET", "/api/workbench/cortex/apply"); },
  // applyApprovedPreview crosses the write boundary: it writes the approved
  // aggregate's files to the project. Only ever called from an explicit,
  // confirmed click — never on load, preview, or approval.
  applyApprovedPreview: function (lockboxRequestId) {
    return request("POST", "/api/workbench/cortex/apply", { lockbox_request_id: lockboxRequestId });
  },
  getValidation: function () { return request("GET", "/api/workbench/cortex/apply/validation"); },
  runValidation: function (applyId, command) {
    var body = { apply_id: applyId };
    if (command) body.command = command;
    return request("POST", "/api/workbench/cortex/apply/validate", body);
  },

  getLaneConversation: function () { return request("GET", "/api/workbench/lane-conversation"); },
  getLaneConversations: function () { return request("GET", "/api/workbench/lane-conversations"); },
  createLaneConversation: function (body) { return request("POST", "/api/workbench/lane-conversation", body); },
  sendLaneConversationMessage: function (convId, message) {
    return request("POST", "/api/workbench/lane-conversation/message", { conversation_id: convId, message: message });
  },
  closeLaneConversation: function (convId) {
    return request("POST", "/api/workbench/lane-conversation/close", { conversation_id: convId });
  },
};

// ---- state ----------------------------------------------------------------
var state = {
  project: null,
  provider: null,
  providerTest: null,
  architectSession: null,   // current ArchitectSession
  architectMessages: [],    // mirror of architectSession.messages
  acceptedSpec: null,       // spec captured when a session is accepted
  cortexTask: null,         // current CortexTask
  lanes: [],                // mirror of cortexTask.lanes
  selectedLane: null,
  laneConversation: null,   // current LaneConversation
  proposals: [],            // CortexLaneProposal list (builder lane proposals)
  aggregate: null,          // current CortexAggregateProposal
  review: null,             // current CortexAggregateReview
  approvals: [],            // LockboxApprovalRequest list
  applyPreview: null,       // current CortexApplyPreview (read-only)
  apply: null,              // current CortexApplyResult (the write)
  validation: null,         // current CortexApplyValidation
  events: [],
  error: "",
  loading: false,
  // supporting fields used by the UI
  sessionCount: 0,
  kind: "architect",        // active Lane Console context kind
  conversations: [],
  view: "context",          // right context panel: context|aggregate|lockbox|apply|log
  setupCollapsed: true,     // frontend-only: collapse setup into the rail once a task exists
};

var KINDS = ["architect", "builder", "reviewer", "validator", "lockbox", "apply"];
var LANE_KINDS = ["builder", "reviewer", "validator"];

// The right context panel switches between these views; the central chat is
// always visible regardless. "context" shows the active context's artifact (the
// Architect spec or the selected lane's detail). Switching is purely frontend
// (CSS via body[data-view]) and persisted, independent of the loop's backend
// state; VIEW_KEY is separate from the selection keys so it can never disturb
// hf.selectedLaneId.v1 / hf.selectedConsoleKind.v1.
var VIEWS = ["context", "aggregate", "lockbox", "apply", "log"];
var VIEW_KEY = "hf.workbenchView.v1";

function applyView() {
  if (document.body) document.body.setAttribute("data-view", state.view);
  var items = document.querySelectorAll(".nav-item");
  for (var i = 0; i < items.length; i++) {
    var v = items[i].getAttribute("data-view");
    if (v === state.view) items[i].classList.add("active");
    else items[i].classList.remove("active");
  }
}

function setView(view) {
  if (VIEWS.indexOf(view) < 0) view = "context";
  state.view = view;
  lsSet(VIEW_KEY, view);
  applyView();
  renderNextHint(); // refresh the jump button now that the active panel changed
}

function restoreView() {
  var v = lsGet(VIEW_KEY);
  // Old persisted values (lanes / lane-detail) are no longer views and simply
  // fall back to the default "context".
  if (v && VIEWS.indexOf(v) >= 0) state.view = v;
  applyView();
}

// Whenever the architect session changes, keep the message/spec mirrors in sync.
function setArchitectSession(session) {
  state.architectSession = session;
  state.architectMessages = (session && session.messages) || [];
  if (session && session.status === "accepted") state.acceptedSpec = session.spec || null;
}

// Whenever the cortex task changes, keep the lanes mirror in sync.
function setCortexTask(task) {
  state.cortexTask = task;
  state.lanes = (task && task.lanes) || [];
}

// ---- helpers --------------------------------------------------------------
function $(id) { return document.getElementById(id); }

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"]/g, function (c) {
    return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
  });
}

function fmtTs(ts) {
  if (!ts) return "";
  try { return new Date(ts).toLocaleTimeString(); } catch (e) { return ts; }
}

function showError(msg) { state.error = msg; var e = $("error"); e.textContent = msg; e.classList.remove("hidden"); }
function clearError() { state.error = ""; var e = $("error"); e.textContent = ""; e.classList.add("hidden"); }

// clearApiKeyInput wipes the API key field so the secret is not left visible in
// the browser after it has been sent to the local backend.
function clearApiKeyInput() {
  var el = $("provider-key");
  if (el) el.value = "";
}

// fail surfaces a failed call and returns true so callers can early-return.
function fail(r, ctx) {
  if (!r.ok) { showError(ctx + ": " + (r.error || ("HTTP " + r.status))); return true; }
  return false;
}

// ---- render ---------------------------------------------------------------
function chip(label, val) { return '<span class="chip"><b>' + esc(label) + "</b> " + esc(val) + "</span>"; }

function renderChips() {
  $("status-chips").innerHTML =
    chip("project", state.project ? state.project.name : "—") +
    chip("provider", state.provider ? state.provider.model : "—") +
    chip("task", state.cortexTask ? ("#" + state.cortexTask.id + " " + state.cortexTask.status) : "—");
}

// loopStatus reads the whole operator-loop state from already-hydrated frontend
// state, so the Run Summary and Next guidance share one source of truth. (The
// readiness helpers it calls are hoisted function declarations defined below.)
function loopStatus() {
  var agg = currentAggregate();
  return {
    project: state.project,
    provider: state.provider,
    providerTest: state.providerTest,
    session: state.architectSession,
    task: state.cortexTask,
    proposedCount: proposedProposalsForTask().length,
    aggregate: agg,
    review: reviewReadiness().review,
    approval: agg ? approvalForAggregate(agg.id) : null,
    preview: previewReadiness().preview,
    applied: appliedResultForAggregate(agg),
    validation: validationReadiness().validation,
  };
}

// loopNext returns the single concise next action across the FULL loop.
function loopNext() {
  var s = loopStatus();
  if (!s.project) return "Open a local project.";
  if (!s.provider) return "Configure a provider.";
  if (!s.session) return "Start an Architect session.";
  if (s.session.status !== "accepted") return "Refine the spec, then Accept it.";
  if (!s.task) return "Create a Cortex task from the accepted spec.";
  if (s.proposedCount === 0) {
    var l = state.selectedLane;
    return (l && l.role === "builder")
      ? "Generate a proposal for the selected Builder lane."
      : "Select a Builder lane, then generate a proposal.";
  }
  if (!s.aggregate) return "Go to Agg and aggregate the Builder proposals.";
  if (!s.review) return "Go to Agg and run the Reviewer.";
  if (s.review.verdict === "revise") return "Reviewer requested changes. Open the Builder revision chat, ask for a revised proposal, then aggregate and review again.";
  if (s.review.verdict !== "approve") return "Reviewer verdict is " + s.review.verdict + " — revise before approval.";
  if (!s.approval) return "Go to Lock and request Lockbox approval.";
  if (s.approval.status === "pending") return "Go to Lock and approve (or reject) the request.";
  if (s.approval.status === "rejected") return "Lockbox rejected — revise and request again.";
  if (!s.preview || s.preview.status !== "ready") return "Go to Lock and preview the apply (read-only).";
  if (!s.applied) return "Go to Apply and explicitly apply the approved preview (writes files).";
  if (!s.validation) return "Go to Apply and run validation.";
  return "Loop complete — applied and validated.";
}

// loopNextTarget returns the stage rail target the next action lives on, or null
// for steps with no single rail destination (setup, "open a builder lane",
// revise/rejected, loop complete). It mirrors loopNext() exactly so the optional
// jump button always matches the hint text. Preview lives in the Lock stage, so
// its target is lockbox — consistent with the hint.
function loopNextTarget() {
  var s = loopStatus();
  if (!s.project || !s.provider || !s.session || s.session.status !== "accepted" || !s.task) return null;
  if (s.proposedCount === 0) return null; // "open a builder lane" — text only
  if (!s.aggregate) return { view: "aggregate", label: "Open Agg" };
  if (!s.review) return { view: "aggregate", label: "Open Agg" };
  if (s.review.verdict === "revise") return { action: "revise", label: "Open Builder revision chat" };
  if (s.review.verdict !== "approve") return null; // reject — no single target
  if (!s.approval) return { view: "lockbox", label: "Open Lock" };
  if (s.approval.status === "pending") return { view: "lockbox", label: "Open Lock" };
  if (s.approval.status === "rejected") return null;
  if (!s.preview || s.preview.status !== "ready") return { view: "lockbox", label: "Open Lock" };
  if (!s.applied) return { view: "apply", label: "Open Apply" };
  if (!s.validation) return { view: "apply", label: "Open Apply" };
  return null; // loop complete
}

// renderNextHint gives the operator a single, obvious next action for the loop,
// plus a small frontend-only jump button to the relevant stage (never an
// auto-switch, never a backend action). The button is omitted when its target
// stage is already on screen.
function renderNextHint() {
  var el = $("next-hint");
  if (!el) return;
  var html = '<span class="next-text">Next: ' + esc(loopNext()) + "</span>";
  var t = loopNextTarget();
  if (t && t.action) {
    html += ' <button class="next-go" data-next-action="' + esc(t.action) + '">' + esc(t.label) + "</button>";
  } else if (t && t.view && t.view !== state.view) {
    html += ' <button class="next-go" data-next-view="' + esc(t.view) + '">' + esc(t.label) + "</button>";
  }
  el.innerHTML = html;
}

function firstBuilderLane() {
  return state.lanes.find(function (x) { return x.role === "builder"; }) || null;
}

// currentStep is the state-driven "do the next thing" model: a one-line
// description plus the action button(s) that progress the workflow RIGHT HERE in
// the chat, so the operator never has to hunt for the next action in a side
// panel. It is derived from the same loopStatus() / readiness helpers as
// loopNext(), and each action maps to an existing frontend handler (no hidden
// backend behavior). Buttons are disabled (not hidden) when their gate is unmet,
// so the current step is always visible and honest.
function currentStep() {
  var s = loopStatus();
  var step = { text: "", actions: [] };

  if (!s.project) { step.text = "Open a local project in Setup (left rail)."; return step; }
  if (!s.provider) { step.text = "Configure a provider in Setup (left rail)."; return step; }
  if (!s.session) { step.text = "Describe a goal in the composer below so the Architect can draft a spec."; return step; }

  var ar = architectReadiness();
  if (s.session.status !== "accepted") {
    step.text = "Refine the spec with the Architect, then accept it.";
    step.actions.push({ label: "Accept spec", action: "accept-spec", disabled: !ar.canAccept });
    return step;
  }
  if (!s.task) {
    step.text = "Spec accepted — create a Cortex task.";
    step.actions.push({ label: "Create Cortex task", action: "create-task", disabled: !ar.canCreateTask });
    return step;
  }

  // A task exists. Reviewer "revise" is checked first so it never dead-ends: it
  // only matches while there's a review for the current aggregate with that
  // verdict (re-aggregating clears it and the normal flow resumes).
  if (s.review && s.review.verdict === "revise") {
    step.text = "Reviewer requested changes — open the Builder revision chat and ask for a revised proposal.";
    step.actions.push({ label: "Open Builder revision chat", action: "revise" });
    var bl = state.selectedLane;
    if (bl && bl.role === "builder" && state.laneConversation && state.laneConversation.status === "active") {
      step.actions.push({ label: "Send revision request", action: "send-conv" });
    }
    if (proposalReadiness().canGenerate) step.actions.push({ label: "Generate revised proposal", action: "gen-proposal" });
    if (aggregateReadiness().canAggregate) step.actions.push({ label: "Re-aggregate", action: "aggregate" });
    return step;
  }

  if (s.proposedCount === 0) {
    var l = state.selectedLane;
    if (l && l.role === "builder") {
      step.text = "Builder lane #" + l.id + " selected — generate its proposal.";
      step.actions.push({ label: "Generate Builder Proposal", action: "gen-proposal", disabled: !proposalReadiness().canGenerate });
    } else {
      var b = firstBuilderLane();
      step.text = "Select a Builder lane to generate a proposal.";
      if (b) step.actions.push({ label: "Select Builder " + b.index, action: "select-builder" });
    }
    return step;
  }
  if (aggregateReadiness().canAggregate) {
    step.text = s.aggregate ? "New proposals — re-aggregate for review." : "Aggregate the Builder proposals.";
    step.actions.push({ label: s.aggregate ? "Re-aggregate" : "Aggregate Proposals", action: "aggregate" });
    return step;
  }
  if (!s.review) {
    step.text = "Run the Reviewer on the aggregate.";
    step.actions.push({ label: "Run Reviewer", action: "review", disabled: !reviewReadiness().canReview });
    return step;
  }
  if (s.review.verdict !== "approve") {
    step.text = "Reviewer verdict is " + s.review.verdict + " — revise the proposal, then review again.";
    return step;
  }
  if (!s.approval) {
    step.text = "Request Lockbox approval for the reviewed aggregate.";
    step.actions.push({ label: "Request Lockbox Approval", action: "request-approval", disabled: !approvalReadiness().canRequest });
    return step;
  }
  if (s.approval.status === "pending") {
    step.text = "Human approval required for the Lockbox request.";
    step.actions.push({ label: "Approve", action: "approve" });
    step.actions.push({ label: "Reject", action: "reject" });
    return step;
  }
  if (s.approval.status === "rejected") {
    step.text = "Lockbox rejected — revise and request approval again.";
    return step;
  }
  if (!s.preview || s.preview.status !== "ready") {
    step.text = "Preview the apply (read-only) before writing.";
    step.actions.push({ label: "Preview Apply", action: "preview", disabled: !previewReadiness().canPreview });
    return step;
  }
  if (!s.applied) {
    step.text = "Write boundary — apply the approved preview to your project.";
    step.actions.push({ label: "Apply Approved Preview", action: "apply", disabled: !applyReadiness().canApply, danger: true });
    return step;
  }
  if (!s.validation) {
    step.text = "Validate the applied changes.";
    step.actions.push({ label: "Run Validation", action: "validate", disabled: !validationReadiness().canValidate });
    return step;
  }
  step.text = "Loop complete — applied and validated. ✓";
  return step;
}

// renderCurrentStep paints the central Current Step action area. Each button
// carries data-step-action, dispatched in wire() to the existing handler.
function renderCurrentStep() {
  var el = $("current-step");
  if (!el) return;
  var step = currentStep();
  var html = '<span class="step-label">Current step</span>' +
    '<span class="step-text">' + esc(step.text) + "</span>";
  if (step.actions && step.actions.length) {
    html += '<span class="step-actions">';
    step.actions.forEach(function (a) {
      html += '<button class="step-btn' + (a.danger ? " step-danger" : "") + '" data-step-action="' + esc(a.action) + '"' +
        (a.disabled ? " disabled" : "") + ">" + esc(a.label) + "</button>";
    });
    html += "</span>";
  }
  el.innerHTML = html;
}

// renderRunSummary lists every loop stage with a compact status, so the operator
// can see where the run stands at a glance — including right after a reload.
function renderRunSummary() {
  var el = $("run-summary");
  if (!el) return;
  var s = loopStatus();
  function row(label, done, val) {
    return '<div class="rs-row ' + (done ? "rs-done" : "rs-todo") + '">' +
      '<span class="rs-label">' + label + "</span>" +
      '<span class="rs-val">' + esc(val) + "</span></div>";
  }
  var providerVal = s.provider
    ? s.provider.model + (s.providerTest ? (s.providerTest.ok ? " · test ok" : " · test failed") : " · untested")
    : "—";
  var html = (s.applied && s.validation) ? '<div class="rs-complete">✓ Loop complete</div>' : "";
  html += row("Project", !!s.project, s.project ? s.project.name : "—");
  html += row("Provider", !!s.provider, providerVal);
  html += row("Architect", !!s.session, s.session ? "#" + s.session.id + " · " + s.session.status : "—");
  html += row("Cortex task", !!s.task, s.task ? "#" + s.task.id + " · " + s.task.status : "—");
  html += row("Proposals", s.proposedCount > 0, s.proposedCount ? s.proposedCount + " proposed" : "—");
  html += row("Aggregate", !!s.aggregate, s.aggregate ? "#" + s.aggregate.id + " · " + s.aggregate.status : "—");
  html += row("Review", !!s.review, s.review ? "#" + s.review.id + " · " + (s.review.verdict || s.review.status) : "—");
  html += row("Lockbox", !!s.approval, s.approval ? "#" + s.approval.id + " · " + s.approval.status : "—");
  html += row("Apply preview", !!s.preview, s.preview ? "#" + s.preview.id + " · " + s.preview.status : "—");
  html += row("Apply", !!s.applied, s.applied ? "#" + s.applied.id + " · " + s.applied.status : "—");
  html += row("Validation", !!s.validation, s.validation ? "#" + s.validation.id + " · " + s.validation.status : "—");
  el.innerHTML = html;
}

// renderHeader keeps the chips, Next hint, Run Summary, setup-collapse, and rail
// badges in sync; call it wherever a setup-affecting state field changes.
function renderHeader() {
  renderChips(); renderNextHint(); renderCurrentStep(); renderRunSummary();
  applySetupCollapse(); renderRailBadges(); renderTickerStatus();
}

// renderTickerStatus fills the compact bottom status: provider, git state, and
// the safety posture (local, apply is always explicit).
function renderTickerStatus() {
  var el = $("ticker-status");
  if (!el) return;
  var parts = [];
  parts.push(state.provider ? (state.provider.model || "provider") : "no provider");
  if (state.project) parts.push(state.project.git ? ("git " + (state.project.current_branch || "?")) : "no git");
  parts.push("local · explicit apply");
  el.textContent = parts.join(" · ");
}

// ---- setup collapse (frontend-only) ---------------------------------------
// Setup (project + provider) demotes to a compact status line in the rail once a
// Cortex task exists, giving the chat priority; "Edit setup" reveals the full
// controls. Before a task it stays fully visible, so first-run setup is never
// hidden. Persisted, but the collapse is only honored once a task exists.
var SETUP_KEY = "hf.setupCollapsed.v1";

function setupCollapsible() {
  return !!state.cortexTask; // setup demotes only after a task exists
}

function setupSummaryText() {
  var p = state.project ? esc(state.project.name) : "no project";
  var pr = state.provider ? esc(state.provider.model || "provider") : "no provider";
  var t = state.cortexTask ? "task #" + esc(state.cortexTask.id) : (state.acceptedSpec ? "spec accepted" : "no task");
  return "✓ " + p + " · " + pr + " · " + t;
}

function applySetupCollapse() {
  var collapsible = setupCollapsible();
  var collapsed = collapsible && state.setupCollapsed;
  if (document.body) document.body.classList.toggle("setup-collapsed", collapsed);
  var btn = $("btn-setup-toggle");
  if (btn) {
    btn.textContent = collapsed ? "Edit setup" : "Hide setup";
    btn.disabled = !collapsible;
    btn.title = collapsible ? "" : "Setup stays open until a Cortex task exists.";
  }
  var sum = $("setup-summary");
  if (sum) sum.innerHTML = setupSummaryText();
}

function toggleSetup() {
  if (!setupCollapsible()) return; // never hide setup before a task exists
  state.setupCollapsed = !state.setupCollapsed;
  lsSet(SETUP_KEY, state.setupCollapsed ? "1" : "0");
  applySetupCollapse();
}

function restoreSetupCollapse() {
  var v = lsGet(SETUP_KEY);
  // Default: collapse setup once a task exists (chat-first). An explicit "0"
  // (the operator opened "Edit setup") keeps it expanded.
  state.setupCollapsed = (v === null) ? true : (v === "1");
  applySetupCollapse();
}

// ---- rail badges (frontend-only) ------------------------------------------
// Small, quiet counters/status dots on the stage rail so the operator can see
// where attention is needed without opening each stage. All derived from the
// already-hydrated loop state.
function setBadge(view, text, cls) {
  var el = $("nav-badge-" + view);
  if (!el) return;
  el.textContent = text || "";
  el.className = "nav-badge" + (text ? " show" : "") + (cls ? " " + cls : "");
}

function renderRailBadges() {
  var s = loopStatus();
  // Agg: how many Builder proposals are in play.
  setBadge("aggregate", s.proposedCount ? String(s.proposedCount) : "", "");
  // Lock: approval status (pending amber / approved green / rejected red).
  if (s.approval) {
    var lc = s.approval.status === "approved" ? "badge-ok" : (s.approval.status === "rejected" ? "badge-bad" : "badge-warn");
    setBadge("lockbox", "•", lc);
  } else setBadge("lockbox", "", "");
  // Apply: applied (amber) → applied + validated (green).
  if (s.applied) setBadge("apply", "•", s.validation ? "badge-ok" : "badge-warn");
  else setBadge("apply", "", "");
  // Log: event count.
  setBadge("log", state.events.length ? String(state.events.length) : "", "");
}

function renderProject() {
  var m = $("project-meta");
  if (!state.project) { m.textContent = "No project open."; return; }
  var p = state.project;
  m.innerHTML = "<div><b>" + esc(p.name) + "</b></div>" +
    '<div class="mono">' + esc(p.path) + "</div>" +
    "<div>git: " + (p.git ? "yes" : "no") + (p.current_branch ? " · " + esc(p.current_branch) : "") + "</div>";
}

function renderProvider() {
  var m = $("provider-meta");
  if (!state.provider) { m.innerHTML = '<div class="empty">No provider configured.</div>'; return; }
  var p = state.provider;
  var s = "<div>model <b>" + esc(p.model) + "</b></div>";
  s += '<div class="mono">' + esc(p.base_url) + "</div>";
  s += "<div>api key: " + (p.api_key_set
    ? '<span class="ok">set</span> <span class="muted">(stored on the local backend; not shown again)</span>'
    : '<span class="bad">unset</span>') + "</div>";

  // Test state: configured-but-untested, test ok, or test failed.
  var t = state.providerTest;
  if (!t) {
    s += '<div class="warn">configured · untested — run Test</div>';
  } else if (t.ok) {
    s += '<div class="ok">test ok · status ' + esc(t.status != null ? t.status : "") +
      (t.model ? " · model " + esc(t.model) : "") + "</div>";
  } else {
    s += '<div class="bad">test failed — ' + esc(t.error || ("HTTP " + (t.status != null ? t.status : "?"))) + "</div>";
  }
  m.innerHTML = s;
}

function renderMsg(m) {
  return '<div class="msg msg-' + esc(m.role) + '"><span class="role">' + esc(m.role) +
    '</span><span class="content">' + esc(m.content) + "</span></div>";
}

function renderSpec(spec, accepted) {
  // Every section is always shown; an empty one says "none" rather than
  // vanishing silently.
  function card(label, arr) {
    var body = (arr && arr.length)
      ? "<ul>" + arr.map(function (x) { return "<li>" + esc(x) + "</li>"; }).join("") + "</ul>"
      : '<span class="spec-none">none</span>';
    return '<div class="spec-card"><div class="spec-card-h">' + label + "</div>" + body + "</div>";
  }
  var head = "";
  if (accepted) head += '<div class="spec-accepted">✓ Accepted spec</div>';
  head += '<div class="spec-goal"><span class="spec-goal-label">goal</span> ' + esc(spec.goal || "—") + "</div>";
  return head +
    '<div class="spec-grid">' +
    card("constraints", spec.constraints) +
    card("affected areas", spec.affected_areas) +
    card("acceptance criteria", spec.acceptance_criteria) +
    card("risks", spec.risks) +
    card("open questions", spec.open_questions) +
    card("suggested lanes", spec.suggested_lanes) +
    "</div>";
}

// architectTaskId returns the Cortex task already created from this session, if
// the backend recorded one (cortex_task_id), else "".
function architectTaskId() {
  var s = state.architectSession;
  if (s && s.cortex_task_id) return s.cortex_task_id;
  if (s && s.status === "accepted" && state.cortexTask) return state.cortexTask.id;
  return "";
}

// architectReadiness centralizes the can-start / can-send / can-accept /
// can-create-task gating plus a single inline hint explaining the first blocker.
function architectReadiness() {
  var s = state.architectSession;
  var hasProject = !!state.project;
  var hasProvider = !!state.provider;
  var active = !!(s && s.status === "active");
  var accepted = !!(s && s.status === "accepted");
  var hasSpec = !!(s && s.spec && s.spec.goal);
  var hasTask = !!architectTaskId();
  var goalEl = $("architect-goal"), msgEl = $("architect-message");
  var goal = goalEl ? goalEl.value.trim() : "";
  var msg = msgEl ? msgEl.value.trim() : "";

  var r = {
    canStart: hasProject && hasProvider && goal.length > 0,
    canSend: hasProject && hasProvider && active && msg.length > 0,
    canAccept: active && hasSpec,
    canCreateTask: accepted && !hasTask,
    hint: "",
  };
  if (!hasProject) r.hint = "Open a project before using the Architect.";
  else if (!hasProvider) r.hint = "Configure a provider before using the Architect.";
  else if (!s) r.hint = "Enter a goal and click Start to open an Architect session.";
  else if (active) r.hint = hasSpec ? "Refine with messages, then Accept the spec." : "Send a message so the Architect drafts a spec.";
  else if (accepted && !hasTask) r.hint = "Spec accepted — create a Cortex task.";
  else if (hasTask) r.hint = "Cortex task #" + architectTaskId() + " created — select a Builder lane or use Current Step to continue.";
  return r;
}

// updateArchitectControls recomputes button-disabled state and the inline hint.
// It is cheap and runs on every keystroke in the goal / message inputs.
function updateArchitectControls() {
  var r = architectReadiness();
  $("btn-architect-start").disabled = !r.canStart;
  $("btn-architect-send").disabled = !r.canSend;
  $("btn-architect-accept").disabled = !r.canAccept;
  $("btn-architect-task").disabled = !r.canCreateTask;
  var hint = $("architect-hint");
  if (hint) { hint.textContent = r.hint; hint.classList.toggle("hidden", !r.hint); }
}

function renderArchitect() {
  var session = state.architectSession;
  var meta = $("architect-meta");
  if (!session) {
    meta.innerHTML = state.sessionCount
      ? '<span class="badge badge-idle">no active session</span> · ' + state.sessionCount + " total"
      : '<span class="badge badge-idle">no session</span>';
  } else {
    var badge = session.status === "accepted"
      ? '<span class="badge badge-ok">accepted ✓</span>'
      : '<span class="badge badge-active">active</span>';
    var line = badge + " · session <b>#" + esc(session.id) + "</b> · " + state.sessionCount + " total";
    line += (session.spec && session.spec.goal) ? ' · <span class="muted">spec ready</span>' : ' · <span class="muted">no spec yet</span>';
    var taskId = architectTaskId();
    if (taskId) line += ' · <span class="ok">Cortex task #' + esc(taskId) + "</span>";
    meta.innerHTML = line;
  }

  var mm = $("architect-messages");
  mm.innerHTML = state.architectMessages.map(renderMsg).join("") || '<div class="empty">No messages yet — describe your goal so the Architect can draft a spec.</div>';
  mm.scrollTop = mm.scrollHeight;

  $("architect-spec").innerHTML = (session && session.spec && session.spec.goal)
    ? renderSpec(session.spec, session.status === "accepted")
    : '<div class="empty">No spec yet — send a message and the Architect will draft one.</div>';

  updateArchitectControls();
}

// ---- lane + conversation helpers ------------------------------------------
function laneRoleCounts(lanes) {
  var c = {};
  (lanes || []).forEach(function (l) { c[l.role] = (c[l.role] || 0) + 1; });
  return c;
}

// truncPath shortens a long path, keeping the more informative tail.
function truncPath(p, max) {
  max = max || 42;
  if (!p || p.length <= max) return p || "";
  return "…" + p.slice(p.length - (max - 1));
}

// byActiveThenRecent sorts active conversations first, then most-recent id.
function byActiveThenRecent(a, b) {
  var an = a.status === "active" ? 1 : 0, bn = b.status === "active" ? 1 : 0;
  if (an !== bn) return bn - an;
  return (parseInt(b.id, 10) || 0) - (parseInt(a.id, 10) || 0);
}

// conversationForLane returns the best existing conversation bound to a lane
// (any kind), or null — used for the lane-card and inspector indicators.
function conversationForLane(laneId) {
  if (!laneId) return null;
  return state.conversations.filter(function (c) {
    return c.context && c.context.lane_id === laneId;
  }).sort(byActiveThenRecent)[0] || null;
}

// currentContextConversation returns the existing conversation matching the
// active console kind and (for lane kinds) the selected lane, or null. It lets
// the Lane Console reuse an existing conversation instead of always creating one.
function currentContextConversation() {
  return state.conversations.filter(function (c) {
    if (!c.context || c.context.kind !== state.kind) return false;
    if (LANE_KINDS.indexOf(state.kind) >= 0) {
      return !!state.selectedLane && c.context.lane_id === state.selectedLane.id;
    }
    return !c.context.lane_id; // architect / lockbox / apply: not bound to a lane
  }).sort(byActiveThenRecent)[0] || null;
}

// syncConversationForContext shows the conversation matching the current
// selection so the drawer always reflects the selected lane / kind.
function syncConversationForContext() {
  state.laneConversation = currentContextConversation();
}

// ---- builder proposal helpers ---------------------------------------------
// proposalForLane returns the most relevant builder proposal for a lane: a
// "proposed" one if present, else the most recent (any status), else null.
function proposalForLane(laneId) {
  if (!laneId) return null;
  return state.proposals.filter(function (p) { return p.lane_id === laneId; })
    .sort(function (a, b) {
      var ap = a.status === "proposed" ? 1 : 0, bp = b.status === "proposed" ? 1 : 0;
      if (ap !== bp) return bp - ap;
      return (parseInt(b.id, 10) || 0) - (parseInt(a.id, 10) || 0);
    })[0] || null;
}

// proposalReadiness gates "Generate Builder Proposal": a builder lane with
// project + provider + task, and not when a proposed proposal already exists
// (the backend also reuses, so this just avoids encouraging duplicates).
function proposalReadiness() {
  var l = state.selectedLane;
  var isBuilder = !!(l && l.role === "builder");
  var existing = isBuilder ? proposalForLane(l.id) : null;
  var hasProposed = !!(existing && existing.status === "proposed");
  var r = {
    isBuilder: isBuilder,
    existing: existing,
    hasProposed: hasProposed,
    canGenerate: isBuilder && !!state.project && !!state.provider && !!state.cortexTask && !hasProposed,
    hint: "",
  };
  if (isBuilder) {
    if (!state.project) r.hint = "Open a project to generate a proposal.";
    else if (!state.provider) r.hint = "Configure a provider to generate a proposal.";
    else if (!state.cortexTask) r.hint = "Create a Cortex task first.";
    else if (hasProposed) r.hint = "Proposal already generated for this lane.";
    else if (existing && existing.status === "failed") r.hint = "Previous attempt failed — generate again.";
    else r.hint = "Generate a Builder proposal for this lane.";
  }
  return r;
}

// renderProposal renders a builder proposal compactly: id, lane/task, status,
// summary, error, and a truncated changed-file list (no full diffs).
function renderProposal(p) {
  var statusCls = p.status === "proposed" ? "ok" : (p.status === "failed" ? "bad" : "muted");
  var html = '<div class="prop-head">proposal <b>#' + esc(p.id) + '</b> · <span class="' + statusCls + '">' + esc(p.status) + "</span></div>";
  html += '<div class="prop-sub">lane #' + esc(p.lane_id) + (p.task_id ? " · task #" + esc(p.task_id) : "") + (p.ts ? " · " + esc(fmtTs(p.ts)) : "") + "</div>";
  if (p.summary) html += '<div class="prop-summary">' + esc(p.summary) + "</div>";
  if (p.error) html += '<div class="bad prop-error">' + esc(p.error) + "</div>";
  var files = p.files || [];
  html += '<div class="prop-files-h">' + files.length + " changed file" + (files.length === 1 ? "" : "s") + "</div>";
  if (files.length) {
    html += '<ul class="prop-files">' + files.map(function (f) {
      return '<li><span class="prop-action">' + esc(f.action) + '</span> <span class="mono">' + esc(truncPath(f.path)) + "</span></li>";
    }).join("") + "</ul>";
  }
  return html;
}

// ---- aggregate + review helpers -------------------------------------------
function proposedProposalsForTask() {
  if (!state.cortexTask) return [];
  return state.proposals.filter(function (p) {
    return p.task_id === state.cortexTask.id && p.status === "proposed";
  });
}

// sameIdSet compares two id lists as sets (order-independent).
function sameIdSet(a, b) {
  a = (a || []).slice().sort();
  b = (b || []).slice().sort();
  if (a.length !== b.length) return false;
  for (var i = 0; i < a.length; i++) { if (a[i] !== b[i]) return false; }
  return true;
}

function reviewerLane() {
  return state.lanes.find(function (x) { return x.role === "reviewer"; }) || null;
}

// currentAggregate returns the current aggregate when it belongs to the current
// task, else null.
function currentAggregate() {
  if (!state.aggregate || !state.cortexTask) return null;
  return state.aggregate.task_id === state.cortexTask.id ? state.aggregate : null;
}

// aggregateReadiness gates "Aggregate Proposals": an aggregate that already
// covers the exact current proposed set disables it (no duplicate); a changed
// proposed set re-enables it.
function aggregateReadiness() {
  var proposed = proposedProposalsForTask();
  var agg = currentAggregate();
  var covered = !!agg && sameIdSet(agg.source_proposal_ids, proposed.map(function (p) { return p.id; }));
  var r = {
    proposedCount: proposed.length,
    agg: agg,
    canAggregate: !!state.cortexTask && proposed.length > 0 && !covered,
    hint: "",
  };
  if (!state.cortexTask) r.hint = "Create a Cortex task first.";
  else if (proposed.length === 0) r.hint = "Generate at least one Builder proposal to aggregate.";
  else if (covered) r.hint = "Proposals already aggregated.";
  else if (agg) r.hint = "New proposals — re-aggregate to include them.";
  else r.hint = "Aggregate the Builder proposals for review.";
  return r;
}

// reviewReadiness gates "Run Reviewer".
function reviewReadiness() {
  var agg = currentAggregate();
  var lane = reviewerLane();
  var review = (state.review && agg && state.review.aggregate_id === agg.id) ? state.review : null;
  var r = {
    agg: agg,
    lane: lane,
    review: review,
    canReview: !!agg && !!lane && !!state.provider && !review,
    hint: "",
  };
  if (!agg) r.hint = "Aggregate proposals before running the Reviewer.";
  else if (!lane) r.hint = "No reviewer lane in this task.";
  else if (!state.provider) r.hint = "Configure a provider to run the Reviewer.";
  else if (review) r.hint = "Reviewer has reviewed this aggregate.";
  else r.hint = "Run the Reviewer on the aggregate.";
  return r;
}

// renderAggregate renders a compact aggregate: id, task, counts, status, and the
// conflict list when present (no full diffs).
function renderAggregate(a) {
  var statusCls = a.status === "aggregated" ? "ok" : (a.status === "conflicted" ? "warn" : (a.status === "failed" ? "bad" : "muted"));
  var src = (a.source_proposal_ids || []).length, files = (a.files || []).length, conf = (a.conflicts || []).length;
  var html = '<div class="agg-head">aggregate <b>#' + esc(a.id) + '</b> · <span class="' + statusCls + '">' + esc(a.status) + "</span></div>";
  html += '<div class="agg-sub">task #' + esc(a.task_id) + " · " + src + " proposal" + (src === 1 ? "" : "s") +
    " · " + files + " file" + (files === 1 ? "" : "s") + " · " + conf + " conflict" + (conf === 1 ? "" : "s") + "</div>";
  if (a.summary) html += '<div class="agg-summary">' + esc(a.summary) + "</div>";
  if (conf) {
    html += '<div class="agg-conf-h bad">conflicts</div><ul class="agg-conf">' +
      a.conflicts.map(function (c) {
        return '<li><span class="mono">' + esc(truncPath(c.path)) + "</span> — " + esc(c.reason) + "</li>";
      }).join("") + "</ul>";
  }
  return html;
}

// renderReview renders a compact review: id, aggregate/task, status, verdict,
// summary, risks (warnings), and recommendations.
function renderReview(rv) {
  var verdictCls = rv.verdict === "approve" ? "ok" : (rv.verdict === "reject" ? "bad" : "warn");
  var html = '<div class="rev-r-head">review <b>#' + esc(rv.id) + '</b> · <span class="' + (rv.status === "failed" ? "bad" : "muted") + '">' + esc(rv.status) + "</span>";
  if (rv.verdict) html += ' · <span class="' + verdictCls + '">' + esc(rv.verdict) + "</span>";
  html += "</div>";
  html += '<div class="rev-r-sub">aggregate #' + esc(rv.aggregate_id) + (rv.task_id ? " · task #" + esc(rv.task_id) : "") + "</div>";
  if (rv.summary) html += '<div class="rev-r-summary">' + esc(rv.summary) + "</div>";
  if (rv.error) html += '<div class="bad rev-r-summary">' + esc(rv.error) + "</div>";
  function list(label, arr) {
    if (!arr || !arr.length) return "";
    return '<div class="rev-list-h">' + label + '</div><ul class="rev-list">' +
      arr.map(function (x) { return "<li>" + esc(x) + "</li>"; }).join("") + "</ul>";
  }
  html += list("risks", rv.risks);
  html += list("recommendations", rv.recommendations);
  return html;
}

// renderReviewPanel refreshes the three focused stages that carry the back half
// of the loop — Aggregate & Review, Lockbox approval, and the Apply write
// boundary — each into its own stage container. Loop actions call this after a
// mutation; the stage rail decides which one is actually on screen.
function renderReviewPanel() {
  renderAggregateView();
  renderLockboxView();
  renderApplyView();
  // Keep the loop-wide guidance fresh after aggregate → apply actions (which
  // re-render these panels but not the header).
  renderNextHint(); renderCurrentStep(); renderRunSummary();
}

// renderAggregateView renders the Aggregate & Review stage (Agg).
function renderAggregateView() {
  var el = $("view-aggregate");
  if (!el) return;
  var head = '<div class="stage-head"><h2>Aggregate &amp; Review</h2></div>';
  if (!state.cortexTask) {
    el.innerHTML = head + '<div class="empty">Create a Cortex task before aggregating proposals.</div>';
    return;
  }
  var ar = aggregateReadiness();
  var rr = reviewReadiness();
  if (ar.proposedCount === 0 && !ar.agg) {
    el.innerHTML = head + '<div class="empty">No Builder proposals yet. Select a Builder lane and generate a proposal first.</div>';
    return;
  }
  var html = head;
  html += '<div class="rev-actions">' +
    '<button class="rev-btn" data-action="aggregate"' + (ar.canAggregate ? "" : " disabled") + ">Aggregate Proposals</button>" +
    '<button class="rev-btn" data-action="review"' + (rr.canReview ? "" : " disabled") + ">Run Reviewer</button>" +
    "</div>";
  html += '<div class="hint">' + esc(ar.agg ? rr.hint : ar.hint) + "</div>";
  html += ar.agg ? renderAggregate(ar.agg) : '<div class="empty">No aggregate yet — aggregate the Builder proposals to review them together.</div>';
  if (rr.review) html += renderReview(rr.review);
  el.innerHTML = html;
}

// ---- lockbox approval + apply preview (the safe write boundary) -----------
// approvalForAggregate finds the Lockbox request for an aggregate, preferring an
// approved one, then pending, then most recent.
function approvalForAggregate(aggId) {
  if (!aggId) return null;
  var pid = "aggregate:" + aggId;
  var matches = state.approvals.filter(function (a) { return a.proposal_id === pid; });
  if (!matches.length) return null;
  matches.sort(function (a, b) {
    var rank = function (s) { return s === "approved" ? 2 : (s === "pending" ? 1 : 0); };
    var ra = rank(a.status), rb = rank(b.status);
    if (ra !== rb) return rb - ra;
    return (parseInt(b.id, 10) || 0) - (parseInt(a.id, 10) || 0);
  });
  return matches[0];
}

// approvalReadiness gates "Request Lockbox Approval": a non-conflicted aggregate
// with an "approve" Reviewer verdict, and no active request already.
function approvalReadiness() {
  var agg = currentAggregate();
  var review = reviewReadiness().review;
  var approval = agg ? approvalForAggregate(agg.id) : null;
  var hasActive = !!(approval && (approval.status === "pending" || approval.status === "approved"));
  var aggOK = !!(agg && agg.status === "aggregated");
  var r = {
    agg: agg,
    approval: approval,
    canRequest: aggOK && !!review && review.verdict === "approve" && !hasActive,
    hint: "",
  };
  if (!agg) r.hint = "Aggregate proposals first.";
  else if (!aggOK) r.hint = "Aggregate has conflicts — resolve before requesting approval.";
  else if (!review) r.hint = "Run the Reviewer before requesting approval.";
  else if (review.verdict !== "approve") r.hint = "Reviewer verdict is " + review.verdict + " — not approved for Lockbox.";
  else if (hasActive) r.hint = "Lockbox request " + approval.status + ".";
  else r.hint = "Request Lockbox approval for the reviewed aggregate.";
  return r;
}

// previewReadiness gates "Preview Apply": an approved Lockbox request for the
// current aggregate. The preview is read-only and never writes files.
function previewReadiness() {
  var agg = currentAggregate();
  var approval = agg ? approvalForAggregate(agg.id) : null;
  var approved = !!(approval && approval.status === "approved");
  var preview = (state.applyPreview && approval && state.applyPreview.lockbox_request_id === approval.id) ? state.applyPreview : null;
  var r = {
    approval: approval,
    preview: preview,
    canPreview: !!agg && approved,
    hint: "",
  };
  if (!agg) r.hint = "Aggregate proposals first.";
  else if (!approval) r.hint = "Request Lockbox approval first.";
  else if (approval.status !== "approved") r.hint = "Approve the Lockbox request to preview the apply.";
  else if (preview) r.hint = "Apply preview ready — review changes, then apply explicitly.";
  else r.hint = "Generate a read-only apply preview.";
  return r;
}

function renderApproval(a) {
  var statusCls = a.status === "approved" ? "ok" : (a.status === "rejected" ? "bad" : "warn");
  var aggId = (a.proposal_id || "").indexOf("aggregate:") === 0 ? a.proposal_id.slice(10) : a.proposal_id;
  var files = (a.files || []).length;
  var html = '<div class="agg-head">lockbox <b>#' + esc(a.id) + '</b> · <span class="' + statusCls + '">' + esc(a.status) + "</span></div>";
  html += '<div class="agg-sub">aggregate #' + esc(aggId) + (a.goal ? " · " + esc(a.goal) : "") + " · " + files + " file" + (files === 1 ? "" : "s") + "</div>";
  if (a.decision_reason) html += '<div class="agg-summary">reason: ' + esc(a.decision_reason) + "</div>";
  return html;
}

function renderPreview(p) {
  var statusCls = p.status === "ready" ? "ok" : (p.status === "blocked" ? "warn" : (p.status === "failed" ? "bad" : "muted"));
  var files = p.files || [];
  var blocked = files.filter(function (f) { return f.status === "blocked" || f.error; }).length;
  var html = '<div class="agg-head">apply preview <b>#' + esc(p.id) + '</b> · <span class="' + statusCls + '">' + esc(p.status) + "</span></div>";
  html += '<div class="agg-sub">aggregate #' + esc(p.aggregate_id) + " · " + files.length + " file" + (files.length === 1 ? "" : "s") + " · " + blocked + " blocked</div>";
  if (p.error) html += '<div class="bad agg-summary">' + esc(p.error) + "</div>";
  if (files.length) {
    html += '<ul class="agg-conf">' + files.map(function (f) {
      var cls = (f.status === "blocked" || f.error) ? "bad" : "muted";
      return '<li><span class="prop-action">' + esc(f.action) + '</span> <span class="mono">' + esc(truncPath(f.path)) +
        '</span> · <span class="' + cls + '">' + esc(f.status) + (f.error ? " (" + esc(f.error) + ")" : "") + "</span></li>";
    }).join("") + "</ul>";
  }
  return html;
}

// appliedResultForAggregate returns the successful apply result for an aggregate,
// or null. The backend applies an aggregate at most once (idempotent).
function appliedResultForAggregate(agg) {
  if (!agg || !state.apply) return null;
  return (state.apply.aggregate_id === agg.id && state.apply.status === "applied") ? state.apply : null;
}

// applyReadiness gates the explicit "Apply Approved Preview" write: an approved
// Lockbox request, a ready apply preview, and no existing applied result.
function applyReadiness() {
  var agg = currentAggregate();
  var approval = agg ? approvalForAggregate(agg.id) : null;
  var approved = !!(approval && approval.status === "approved");
  var preview = previewReadiness().preview;
  var ready = !!(preview && preview.status === "ready");
  var applied = appliedResultForAggregate(agg);
  var r = {
    agg: agg,
    approval: approval,
    applied: applied,
    canApply: approved && ready && !applied,
    hint: "",
  };
  if (!agg) r.hint = "";
  else if (!approved) r.hint = "Approve the Lockbox request first.";
  else if (!preview) r.hint = "Generate an apply preview first.";
  else if (!ready) r.hint = "Apply preview is " + preview.status + " — resolve before applying.";
  else if (applied) r.hint = "Already applied (#" + applied.id + ").";
  else r.hint = "Apply writes the approved files to your project on disk.";
  return r;
}

// validationReadiness gates "Run Validation" — only after a successful apply.
function validationReadiness() {
  var agg = currentAggregate();
  var applied = appliedResultForAggregate(agg);
  var validation = (state.validation && applied && state.validation.apply_id === applied.id) ? state.validation : null;
  var r = {
    applied: applied,
    validation: validation,
    canValidate: !!applied,
    hint: "",
  };
  if (!applied) r.hint = "Validation runs after a successful apply.";
  else if (validation) r.hint = "Validation " + validation.status + ".";
  else r.hint = "Run a bounded validation command in the project.";
  return r;
}

function renderApplyResult(a) {
  var statusCls = a.status === "applied" ? "ok" : (a.status === "failed" ? "bad" : "muted");
  var files = a.files || [];
  var failed = files.filter(function (f) { return f.status === "failed" || f.error; }).length;
  var html = '<div class="agg-head">apply <b>#' + esc(a.id) + '</b> · <span class="' + statusCls + '">' + esc(a.status) + "</span>" +
    (a.ts ? ' · <span class="muted">' + esc(fmtTs(a.ts)) + "</span>" : "") + "</div>";
  html += '<div class="agg-sub">aggregate #' + esc(a.aggregate_id) + " · " + files.length + " file" + (files.length === 1 ? "" : "s") + " written · " + failed + " failed</div>";
  if (a.error) html += '<div class="bad agg-summary">' + esc(a.error) + "</div>";
  if (files.length) {
    html += '<ul class="agg-conf">' + files.map(function (f) {
      var cls = (f.status === "failed" || f.error) ? "bad" : "ok";
      return '<li><span class="prop-action">' + esc(f.action) + '</span> <span class="mono">' + esc(truncPath(f.path)) +
        '</span> · <span class="' + cls + '">' + esc(f.status) + (f.error ? " (" + esc(f.error) + ")" : "") + "</span></li>";
    }).join("") + "</ul>";
  }
  return html;
}

function renderValidation(v) {
  var statusCls = v.status === "passed" ? "ok" : (v.status === "failed" ? "bad" : "warn");
  var html = '<div class="agg-head">validation <b>#' + esc(v.id) + '</b> · <span class="' + statusCls + '">' + esc(v.status) +
    '</span> · <span class="muted">exit ' + esc(v.exit_code) + "</span></div>";
  if (v.command) html += '<div class="agg-sub mono">' + esc(v.command) + "</div>";
  if (v.error) html += '<div class="bad agg-summary">' + esc(v.error) + "</div>";
  function out(label, s) {
    if (!s) return "";
    var lines = String(s).split("\n").slice(0, 6).join("\n");
    return '<div class="rev-list-h">' + label + '</div><pre class="val-out">' + esc(lines) + "</pre>";
  }
  html += out("stdout", v.stdout);
  html += out("stderr", v.stderr);
  return html;
}

// renderLockboxView renders the Lockbox approval + read-only preview stage
// (Lock). No writes happen here — preview is read-only and approval is the
// human gate that unlocks the Apply stage.
function renderLockboxView() {
  var el = $("view-lockbox");
  if (!el) return;
  var apr = approvalReadiness();
  var pvr = previewReadiness();
  var html = '<div class="stage-head"><h2>Lockbox Approval</h2></div>';
  if (!apr.agg) {
    el.innerHTML = html + '<div class="empty">Aggregate the Builder proposals (Agg) before requesting Lockbox approval.</div>';
    return;
  }
  html += '<div class="rev-actions">';
  html += '<button class="rev-btn" data-action="request-approval"' + (apr.canRequest ? "" : " disabled") + ">Request Lockbox Approval</button>";
  if (apr.approval && apr.approval.status === "pending") {
    html += '<button class="rev-btn" data-action="approve">Approve</button>';
    html += '<button class="rev-btn" data-action="reject">Reject</button>';
  }
  html += '<button class="rev-btn" data-action="preview"' + (pvr.canPreview ? "" : " disabled") + ' title="Read-only — never writes files">Preview Apply (read-only)</button>';
  html += "</div>";
  html += '<div class="hint">' + esc(apr.approval ? pvr.hint : apr.hint) + "</div>";
  if (apr.approval) html += renderApproval(apr.approval);
  if (pvr.preview) html += renderPreview(pvr.preview);
  el.innerHTML = html;
}

// renderApplyView renders the explicit write boundary stage (Apply): the
// guarded, confirmed apply, then optional post-apply validation. Apply is never
// automatic.
function renderApplyView() {
  var el = $("view-apply");
  if (!el) return;
  var apr = approvalReadiness();
  var html = '<div class="stage-head"><h2>Apply &amp; Validation</h2></div>';
  if (!apr.agg) {
    el.innerHTML = html + '<div class="empty">Approve a Lockbox request and preview it (Lock) before applying.</div>';
    return;
  }
  var ar = applyReadiness();
  var vr = validationReadiness();
  html += '<div class="write-boundary">';
  html += '<div class="write-label">Write boundary — Apply writes files to your project</div>';
  html += '<div class="rev-actions">';
  html += '<button class="rev-btn apply-btn" data-action="apply"' + (ar.canApply ? "" : " disabled") + ' title="Writes the approved files to your project on disk">Apply Approved Preview</button>';
  html += '<input id="validate-command" class="val-cmd" placeholder="validation command (optional)">';
  html += '<button class="rev-btn" data-action="validate"' + (vr.canValidate ? "" : " disabled") + ">Run Validation</button>";
  html += "</div>";
  html += '<div class="hint">' + esc(ar.applied ? vr.hint : ar.hint) + "</div>";
  if (ar.applied) html += renderApplyResult(ar.applied);
  if (vr.validation) html += renderValidation(vr.validation);
  html += "</div>";
  el.innerHTML = html;
}

// renderTaskSummary renders the compact Cortex task summary for the board head.
function renderTaskSummary() {
  var t = state.cortexTask;
  var counts = laneRoleCounts(state.lanes);
  var rolesStr = Object.keys(counts).map(function (k) { return k + " " + counts[k]; }).join(" · ");
  var head = '<span class="task-id">#' + esc(t.id) + "</span> " +
    '<span class="badge badge-active">' + esc(t.status) + "</span>" +
    ' · <span class="task-goal">' + esc(t.goal) + "</span>";
  var sub = state.lanes.length + " lanes";
  if (rolesStr) sub += " · " + esc(rolesStr);
  if (t.ts) sub += " · created " + esc(fmtTs(t.ts));
  if (state.architectSession && state.architectSession.id) sub += " · from session #" + esc(state.architectSession.id);
  return head + '<div class="task-sub">' + sub + "</div>";
}

function renderBoard() {
  var meta = $("board-meta");
  var wrap = $("lanes");
  if (!state.cortexTask) {
    meta.textContent = "Accept an Architect spec, then create a Cortex task.";
    wrap.innerHTML = "";
    return;
  }
  meta.innerHTML = renderTaskSummary();
  wrap.innerHTML = state.lanes.map(function (l) {
    var sel = state.selectedLane && state.selectedLane.id === l.id ? " selected" : "";
    var conv = conversationForLane(l.id);
    var convTag = conv
      ? '<span class="lane-conv status-' + esc(conv.status) + '">conv · ' + esc(conv.status) + "</span>"
      : "";
    var path = l.workspace_path || l.worktree_branch || "";
    var pathTag = path ? '<div class="lane-path mono">' + esc(truncPath(path)) + "</div>" : "";
    return '<button class="lane role-' + esc(l.role) + sel + '" data-lane="' + esc(l.id) + '">' +
      '<div class="lane-head"><span class="lane-role">' + esc(l.role) + " " + l.index + "</span>" + convTag + "</div>" +
      '<div class="lane-task">' + esc(l.task) + "</div>" +
      pathTag +
      '<div class="lane-status">' + esc(l.status) + "</div>" +
      "</button>";
  }).join("") || '<div class="empty">No lanes.</div>';
}

function renderInspector() {
  var b = $("inspector-body");
  if (!state.selectedLane) {
    b.innerHTML = '<div class="ins-row"><b>context</b> ' + esc(state.kind) + "</div>" +
      '<div class="empty">No lane selected. Pick a lane on the board to see its role, status, branch, conversation, and proposal here.</div>';
    return;
  }
  var l = state.selectedLane;
  var conv = conversationForLane(l.id);
  var rows =
    '<div class="ins-row"><b>lane</b> ' + esc(l.id) + "</div>" +
    '<div class="ins-row"><b>role</b> ' + esc(l.role) + "</div>" +
    '<div class="ins-row"><b>index</b> ' + esc(l.index) + "</div>" +
    '<div class="ins-row"><b>status</b> ' + esc(l.status) + "</div>" +
    '<div class="ins-row"><b>task</b> ' + esc(l.task) + "</div>" +
    (state.cortexTask ? '<div class="ins-row"><b>related task</b> #' + esc(state.cortexTask.id) + "</div>" : "") +
    (l.worktree_branch ? '<div class="ins-row"><b>branch</b> ' + esc(l.worktree_branch) + "</div>" : "") +
    (l.workspace_path ? '<div class="ins-row"><b>workspace</b> <span class="mono">' + esc(l.workspace_path) + "</span></div>" : "") +
    (conv
      ? '<div class="ins-row"><b>conversation</b> #' + esc(conv.id) + ' · <span class="status-' + esc(conv.status) + '">' + esc(conv.status) + "</span></div>"
      : '<div class="ins-row"><b>conversation</b> <span class="muted">none — open one from the chat</span></div>');

  if (l.role === "builder") {
    var pr = proposalReadiness();
    rows += '<div class="ins-prop">' +
      '<div class="ins-prop-head"><b>builder proposal</b>' +
      '<button class="prop-btn" data-action="gen-proposal"' + (pr.canGenerate ? "" : " disabled") + ">Generate Builder Proposal</button></div>" +
      (pr.existing ? renderProposal(pr.existing) : '<div class="empty">No proposal yet — generate one for this builder lane.</div>') +
      (pr.hint ? '<div class="hint">' + esc(pr.hint) + "</div>" : "") +
      "</div>";
  }

  if (l.role === "reviewer") {
    var rr = reviewReadiness();
    rows += '<div class="ins-prop"><div class="ins-prop-head"><b>reviewer</b></div>' +
      (rr.review ? renderReview(rr.review) : '<div class="empty">No review yet. Aggregate proposals, then Run Reviewer.</div>') +
      "</div>";
  }

  if (l.role === "validator") {
    var vr = validationReadiness();
    rows += '<div class="ins-prop"><div class="ins-prop-head"><b>validator</b></div>' +
      (vr.validation ? renderValidation(vr.validation) : '<div class="empty">No validation yet. Apply an approved preview, then Run Validation (Apply stage).</div>') +
      "</div>";
  }

  if (l.role === "architect") {
    rows += '<div class="ins-prop"><div class="ins-prop-head"><b>architect</b></div>' +
      (state.acceptedSpec ? '<div class="spec-accepted">spec accepted</div>' : "") +
      (state.cortexTask
        ? '<div class="prop-sub">task #' + esc(state.cortexTask.id) + " · " + esc(state.cortexTask.goal) + "</div>"
        : '<div class="empty">No Cortex task yet — accept a spec, then create one.</div>') +
      "</div>";
  }
  b.innerHTML = rows;
}

function cap(s) { return s ? s.charAt(0).toUpperCase() + s.slice(1) : s; }

// ---- chat surface ---------------------------------------------------------
// Chat is the primary work surface. The center routes by the active context
// kind: the "architect" kind shows the Architect spec session; every other kind
// shows that lane / context conversation. body[data-chat] drives which composer
// and stream are visible; body[data-arch] hides the Start row once a session
// exists. The Architect spec artifact and lane artifacts live in the right
// context panel, not in the chat.
function renderChat() {
  var architect = (state.kind === "architect");
  if (document.body) {
    document.body.setAttribute("data-chat", architect ? "architect" : "lane");
    document.body.setAttribute("data-arch", state.architectSession ? "session" : "nosession");
  }
  renderChatHead();
  renderReviseBanner();
  renderCurrentStep();
}

function renderChatHead() {
  var titleEl = $("chat-title"), subEl = $("chat-sub");
  if (!titleEl || !subEl) return;
  if (state.kind === "architect") {
    titleEl.textContent = "Architect";
    var s = state.architectSession;
    subEl.textContent = s
      ? ("session #" + s.id + " · " + s.status + (architectTaskId() ? " · task #" + architectTaskId() : ""))
      : "describe a goal to draft a spec";
  } else if (LANE_KINDS.indexOf(state.kind) >= 0) {
    var l = state.selectedLane;
    titleEl.textContent = cap(state.kind) + (l ? " lane #" + l.id : " lane");
    subEl.textContent = l ? (l.role + " " + l.index + " · " + l.status) : ("select a " + state.kind + " lane");
  } else { // lockbox / apply — task-level, not lane-bound
    titleEl.textContent = cap(state.kind);
    subEl.textContent = "task-level " + state.kind + " conversation";
  }
}

// ---- reviewer "revise" forward path ---------------------------------------
// reviseReview returns the current review only when its verdict is "revise" (a
// dead end without a path forward), else null.
function reviseReview() {
  var agg = currentAggregate();
  var rv = (state.review && agg && state.review.aggregate_id === agg.id) ? state.review : null;
  return (rv && rv.verdict === "revise") ? rv : null;
}

// builderLaneForRevision picks the Builder lane to revise: a builder lane with a
// proposed proposal (those are what the aggregate was built from), else any
// builder lane with a proposal, else the first builder lane.
function builderLaneForRevision() {
  var builders = state.lanes.filter(function (x) { return x.role === "builder"; });
  if (!builders.length) return null;
  var proposed = builders.filter(function (l) { var p = proposalForLane(l.id); return p && p.status === "proposed"; });
  var withAny = builders.filter(function (l) { return !!proposalForLane(l.id); });
  return proposed[0] || withAny[0] || builders[0];
}

// revisionPrompt builds the prefilled revision request from the reviewer's
// summary / risks / recommendations. It is only ever placed in the composer —
// never auto-sent.
function revisionPrompt(rv) {
  function block(label, val) {
    var body = (val && val.length)
      ? (Array.isArray(val) ? val.map(function (x) { return "- " + x; }).join("\n") : String(val))
      : "(none)";
    return label + ":\n" + body;
  }
  return "Please revise your proposal based on the reviewer feedback:\n\n" +
    block("Summary", rv.summary) + "\n\n" +
    block("Risks", rv.risks) + "\n\n" +
    block("Recommendations", rv.recommendations) + "\n\n" +
    "Return a revised proposal focused on the reviewer's concerns.";
}

// renderReviseBanner shows the forward path near the composer when the reviewer
// asked for changes. The button only selects / opens / prefills (handled in
// openBuilderRevision) — it never approves, applies, or writes.
function renderReviseBanner() {
  var el = $("revise-banner");
  if (!el) return;
  var rv = reviseReview();
  if (!rv) { el.classList.add("hidden"); el.innerHTML = ""; return; }
  el.classList.remove("hidden");
  el.innerHTML = '<span class="revise-text">Reviewer requested changes — revise the Builder proposal, then aggregate and review again.</span>' +
    ' <button class="revise-btn" data-action="open-revision">Open Builder revision chat</button>';
}

// openBuilderRevision is the reviewer-revise forward path: select a builder lane,
// open/reuse its conversation, switch the central chat to it, and prefill the
// composer with the reviewer feedback. No approval, no apply, no resolve.
async function openBuilderRevision() {
  var rv = reviseReview();
  if (!rv) return;
  var lane = builderLaneForRevision();
  if (!lane) { showError("revision: no builder lane found"); return; }
  state.selectedLane = lane;
  state.kind = "builder";
  persistSelection();
  setView("context");          // right panel shows this builder lane's artifact
  syncConversationForContext();
  await newConversation();      // open or reuse the builder lane conversation
  var box = $("conv-message");
  if (box) { box.value = revisionPrompt(rv); }
  updateConsoleControls();      // enable Send now that there is text + an open conversation
  renderChat();
  if (box) box.focus();
}

// kindAvailable reports whether a context kind can be used now: non-lane kinds
// always, lane kinds only when the task has a lane of that role.
function kindAvailable(k) {
  if (LANE_KINDS.indexOf(k) < 0) return true;
  return state.lanes.some(function (x) { return x.role === k; });
}

// consoleReadiness centralizes the Lane Console gating: which controls are
// enabled, the Open/Reuse label, and the single inline hint for the first
// blocker.
function consoleReadiness() {
  var laneBound = LANE_KINDS.indexOf(state.kind) >= 0;
  var laneOfKind = kindAvailable(state.kind);
  var laneNeeded = laneBound && !state.selectedLane;
  var conv = state.laneConversation;
  var active = !!(conv && conv.status === "active");
  var existing = currentContextConversation();
  var hasReusable = !!(existing && existing.status !== "closed");
  var msgEl = $("conv-message");
  var msg = msgEl ? msgEl.value.trim() : "";

  var r = {
    laneBound: laneBound,
    canOpen: !laneNeeded && laneOfKind,
    canSend: active && msg.length > 0,
    canClose: active,
    openLabel: hasReusable ? "Reuse Conversation" : "Open Conversation",
    hint: "",
  };
  if (laneBound && !laneOfKind) r.hint = "No " + state.kind + " lane in this task — pick another kind.";
  else if (laneNeeded) r.hint = "Select a " + state.kind + " lane on the board to bind a conversation.";
  else if (!conv) r.hint = hasReusable
    ? "A conversation already exists — click Reuse Conversation to open it."
    : "Open a conversation for this " + (laneBound ? "lane" : "context") + ".";
  else if (conv.status === "closed") r.hint = "This conversation is closed — open a new one.";
  else if (!msg) r.hint = "Type a message and press Enter to send.";
  return r;
}

// updateConsoleControls applies consoleReadiness to the buttons and hint. It is
// cheap and also runs on every keystroke in the message input.
function updateConsoleControls() {
  var r = consoleReadiness();
  var openBtn = $("btn-conv-new");
  if (openBtn) { openBtn.textContent = r.openLabel; openBtn.disabled = !r.canOpen; }
  $("btn-conv-send").disabled = !r.canSend;
  $("btn-conv-close").disabled = !r.canClose;
  var hint = $("console-hint");
  if (hint) { hint.textContent = r.hint; hint.classList.toggle("hidden", !r.hint); }
}

// renderDrawerScope renders the compact contextual header: kind, lane-bound vs
// general, selected lane, task id, and conversation id/status.
function renderDrawerScope() {
  var laneBound = LANE_KINDS.indexOf(state.kind) >= 0;
  var parts = ['kind <b>' + esc(state.kind) + "</b>"];
  parts.push(laneBound
    ? '<span class="scope-tag scope-lane">lane-bound</span>'
    : '<span class="scope-tag scope-general">general</span>');
  if (laneBound) {
    if (state.selectedLane) {
      var l = state.selectedLane;
      parts.push("lane <b>#" + esc(l.id) + "</b> " + esc(l.role) + " · " + esc(l.status));
    } else {
      parts.push('<span class="muted">no ' + esc(state.kind) + " lane selected</span>");
    }
  }
  if (state.cortexTask) parts.push("task #" + esc(state.cortexTask.id));
  parts.push(state.laneConversation
    ? "conversation #" + esc(state.laneConversation.id) + ' · <span class="status-' +
      esc(state.laneConversation.status) + '">' + esc(state.laneConversation.status) + "</span>"
    : '<span class="muted">no conversation</span>');
  if (state.selectedLane && state.selectedLane.role === "builder") {
    var prop = proposalForLane(state.selectedLane.id);
    parts.push(prop ? "proposal #" + esc(prop.id) + " · " + esc(prop.status) : '<span class="muted">no proposal</span>');
  }
  if (state.selectedLane && state.selectedLane.role === "reviewer") {
    var agg = currentAggregate();
    var rvw = (state.review && agg && state.review.aggregate_id === agg.id) ? state.review : null;
    parts.push(rvw
      ? "review #" + esc(rvw.id) + " · " + esc(rvw.status) + (rvw.verdict ? " · " + esc(rvw.verdict) : "")
      : '<span class="muted">no review</span>');
  }
  if (state.kind === "lockbox") {
    var lagg = currentAggregate();
    var ap = lagg ? approvalForAggregate(lagg.id) : null;
    parts.push(ap ? "approval #" + esc(ap.id) + " · " + esc(ap.status) : '<span class="muted">no approval</span>');
  }
  if (state.kind === "apply") {
    parts.push(state.applyPreview
      ? "preview #" + esc(state.applyPreview.id) + " · " + esc(state.applyPreview.status)
      : '<span class="muted">no preview</span>');
    var applied = appliedResultForAggregate(currentAggregate());
    if (applied) parts.push("apply #" + esc(applied.id) + " · " + esc(applied.status));
    var v = (state.validation && applied && state.validation.apply_id === applied.id) ? state.validation : null;
    if (v) parts.push("validation · " + esc(v.status));
  }
  $("drawer-scope").innerHTML = parts.join(" · ");
}

function renderDrawer() {
  $("kind-chips").innerHTML = KINDS.map(function (k) {
    var cls = "kind" + (k === state.kind ? " active" : "") + (kindAvailable(k) ? "" : " unavailable");
    var title = kindAvailable(k) ? "" : ' title="No ' + esc(k) + ' lane in this task"';
    return '<button class="' + cls + '" data-kind="' + k + '"' + title + ">" + k + "</button>";
  }).join("");

  renderDrawerScope();

  var laneBound = LANE_KINDS.indexOf(state.kind) >= 0;
  var msgs = (state.laneConversation && state.laneConversation.messages) || [];
  var cm = $("conv-messages");
  cm.innerHTML = msgs.length
    ? msgs.map(renderMsg).join("")
    : (state.laneConversation
      ? '<div class="empty">No messages yet. Type below to start.</div>'
      : '<div class="empty">No conversation open for this ' + (laneBound ? "lane" : "context") + ".</div>");
  cm.scrollTop = cm.scrollHeight;

  updateConsoleControls();
}

function renderEvents() {
  var list = $("event-list");
  if (list) {
    var evs = state.events.slice().reverse(); // newest first
    list.innerHTML = evs.map(function (e) {
      return '<div class="event"><span class="ev-type">' + esc(e.type) + "</span>" +
        '<span class="ev-msg">' + esc(e.message || "") + "</span>" +
        '<span class="ev-ts">' + esc(fmtTs(e.ts)) + "</span></div>";
    }).join("") || '<div class="empty">No events.</div>';
  }
  renderTicker();
  renderRailBadges(); // keep the Log event-count badge fresh on each poll
}

// renderTicker keeps the thin bottom ticker showing the single latest event, so
// the full event stream can stay tucked behind the Log stage without the
// operator losing the live pulse of the run.
function renderTicker() {
  var el = $("ticker-msg");
  if (!el) return;
  var evs = state.events;
  if (!evs || !evs.length) { el.textContent = "No events yet."; return; }
  var e = evs[evs.length - 1]; // newest is last
  el.textContent = (e.type ? e.type + " — " : "") + (e.message || "");
}

function renderAll() {
  renderHeader(); renderProject(); renderProvider(); renderArchitect();
  renderBoard(); renderReviewPanel(); renderInspector(); renderDrawer(); renderChat(); renderEvents();
  applyView();
}

// ---- actions --------------------------------------------------------------
async function refreshEvents() {
  var r = await api.getEvents();
  if (r.ok && Array.isArray(r.data)) { state.events = r.data; renderEvents(); }
}

async function openProject() {
  var path = $("project-path").value.trim();
  if (!path) { showError("project: path is required"); return; }
  var r = await api.openProject(path);
  if (fail(r, "open project")) return;
  clearError(); state.project = r.data; renderProject(); renderHeader(); refreshEvents();
}

async function saveProvider() {
  var config = {
    base_url: $("provider-base").value.trim(),
    api_key: $("provider-key").value,
    model: $("provider-model").value.trim(),
  };
  var r = await api.saveProvider(config);
  if (fail(r, "save provider")) return;
  clearError();
  state.provider = r.data;
  state.providerTest = null;   // freshly saved -> configured but untested
  clearApiKeyInput();          // never leave the key visible in the browser
  renderProvider(); renderHeader(); refreshEvents();
}

async function testProvider() {
  var r = await api.testProvider();
  if (r.data && typeof r.data === "object") {
    state.providerTest = r.data;
    clearError();
  } else {
    state.providerTest = { ok: r.ok, error: r.error || ("HTTP " + r.status) };
    if (!r.ok) showError("test provider: " + (r.error || r.status));
  }
  renderProvider(); refreshEvents();
}

async function startSession() {
  var goal = $("architect-goal").value.trim();
  if (!goal) { showError("architect: goal is required"); return; }
  var r = await api.createArchitectSession(goal);
  if (fail(r, "start session")) return;
  clearError(); setArchitectSession(r.data); await loadSessions(); renderArchitect(); renderHeader(); refreshEvents();
}

async function loadSessions() {
  var r = await api.getArchitectSessions();
  if (r.ok && Array.isArray(r.data)) state.sessionCount = r.data.length;
}

async function sendArchitect() {
  if (!state.architectSession) { showError("architect: start a session first"); return; }
  var msg = $("architect-message").value.trim();
  if (!msg) { showError("architect: message is required"); return; }
  var r = await api.sendArchitectMessage(state.architectSession.id, msg);
  // On provider failure the backend still returns the updated session (with a
  // system error turn) as JSON; reflect it either way.
  if (r.data && typeof r.data === "object") setArchitectSession(r.data);
  if (fail(r, "architect message")) { renderArchitect(); refreshEvents(); return; }
  clearError(); $("architect-message").value = ""; renderArchitect(); refreshEvents();
}

async function acceptSpec() {
  if (!state.architectSession) { showError("architect: no session"); return; }
  var r = await api.acceptArchitectSpec(state.architectSession.id);
  if (fail(r, "accept spec")) return;
  clearError(); setArchitectSession(r.data); renderArchitect(); refreshEvents();
}

async function createTask() {
  if (!state.architectSession) { showError("architect: no session"); return; }
  if (architectTaskId()) { showError("architect: a Cortex task already exists for this session"); return; }
  var r = await api.createCortexTask(state.architectSession.id);
  if (fail(r, "create cortex task")) return;
  clearError(); setCortexTask(r.data);
  // Chat-first: stay in the Architect chat after creating the task; the operator
  // picks a lane when ready, which switches the chat to that lane.
  // Record the task on the session locally so the panel reflects it without a
  // reload (the backend already persisted cortex_task_id).
  if (state.architectSession && r.data) state.architectSession.cortex_task_id = r.data.id;
  // renderReviewPanel refreshes the Agg/Lock/Apply context panels so they reflect
  // the new task instead of their stale "no task" empty state.
  renderBoard(); renderInspector(); renderDrawer(); renderChat(); renderReviewPanel(); renderArchitect(); renderHeader(); refreshEvents();
}

function selectLane(id) {
  var l = state.lanes.find(function (x) { return x.id === id; });
  if (!l) return;
  state.selectedLane = l; state.kind = l.role;
  syncConversationForContext(); // show this lane's conversation if one exists
  persistSelection();
  setView("context"); // the right panel shows this lane's artifact; chat switches to it
  renderBoard(); renderInspector(); renderDrawer(); renderChat(); renderHeader();
}

function setKind(k) {
  state.kind = k;
  if (LANE_KINDS.indexOf(k) >= 0) {
    if (!(state.selectedLane && state.selectedLane.role === k)) {
      state.selectedLane = state.lanes.find(function (x) { return x.role === k; }) || null;
    }
  } else {
    state.selectedLane = null;
  }
  syncConversationForContext();
  persistSelection();
  // The right context panel follows the chat context: lockbox/apply have their
  // own task-level panels; everything else shows the "context" artifact.
  setView(k === "lockbox" ? "lockbox" : (k === "apply" ? "apply" : "context"));
  renderBoard(); renderInspector(); renderDrawer(); renderChat(); renderHeader();
}

async function newConversation() {
  // Prefer an existing open conversation for this lane / kind over creating a
  // duplicate; this is the "reuse instead of always New" behavior.
  var existing = currentContextConversation();
  if (existing && existing.status !== "closed") {
    clearError(); state.laneConversation = existing; renderDrawer(); renderHeader();
    return;
  }
  var body = { kind: state.kind };
  if (LANE_KINDS.indexOf(state.kind) >= 0) {
    if (!state.selectedLane) { showError("lane console: select a " + state.kind + " lane first"); return; }
    body.lane_id = state.selectedLane.id;
    if (state.cortexTask) body.task_id = state.cortexTask.id;
  }
  var r = await api.createLaneConversation(body);
  if (fail(r, "new conversation")) return;
  clearError(); state.laneConversation = r.data;
  await loadConversations(); // refresh so lane cards / inspector show the new conversation
  renderBoard(); renderInspector(); renderDrawer(); renderHeader(); refreshEvents();
}

async function sendConversation() {
  if (!state.laneConversation) { showError("lane console: start a conversation first"); return; }
  var msg = $("conv-message").value.trim();
  if (!msg) { showError("lane console: message is required"); return; }
  var r = await api.sendLaneConversationMessage(state.laneConversation.id, msg);
  if (r.data && typeof r.data === "object") state.laneConversation = r.data;
  if (fail(r, "lane message")) { renderDrawer(); refreshEvents(); return; }
  clearError(); $("conv-message").value = ""; renderDrawer(); refreshEvents();
}

async function closeConversation() {
  if (!state.laneConversation) return;
  var r = await api.closeLaneConversation(state.laneConversation.id);
  if (fail(r, "close conversation")) return;
  clearError(); state.laneConversation = r.data;
  await loadConversations();
  renderBoard(); renderInspector(); renderDrawer(); renderHeader(); refreshEvents();
}

async function loadConversations() {
  var r = await api.getLaneConversations();
  if (r.ok && Array.isArray(r.data)) state.conversations = r.data;
}

async function loadProposals() {
  var r = await api.getLaneProposals();
  if (r.ok && Array.isArray(r.data)) state.proposals = r.data;
}

async function generateProposal() {
  var l = state.selectedLane;
  if (!l || l.role !== "builder") { showError("proposal: select a builder lane first"); return; }
  if (!state.cortexTask) { showError("proposal: create a Cortex task first"); return; }
  var existing = proposalForLane(l.id);
  if (existing && existing.status === "proposed") { showError("proposal: one already exists for this lane"); return; }
  var r = await api.generateLaneProposal(state.cortexTask.id, l.id);
  // On provider failure the backend returns 502 with the stored (failed)
  // proposal; reload either way so the inspector reflects it.
  await loadProposals();
  if (fail(r, "generate proposal")) { renderInspector(); renderDrawer(); refreshEvents(); return; }
  clearError();
  renderInspector(); renderDrawer(); renderReviewPanel(); refreshEvents();
}

async function loadAggregate() {
  var r = await api.getAggregate();
  if (r.ok) state.aggregate = r.data; else if (r.status === 404) state.aggregate = null;
}

async function loadReview() {
  var r = await api.getReview();
  if (r.ok) state.review = r.data; else if (r.status === 404) state.review = null;
}

async function aggregateProposals() {
  if (!state.cortexTask) { showError("aggregate: create a Cortex task first"); return; }
  var r = await api.createAggregate(state.cortexTask.id);
  if (fail(r, "aggregate proposals")) return;
  clearError(); state.aggregate = r.data;
  renderReviewPanel(); renderBoard(); renderInspector(); renderDrawer(); refreshEvents();
}

async function runReviewer() {
  var agg = currentAggregate();
  if (!agg) { showError("review: aggregate proposals first"); return; }
  var lane = reviewerLane();
  if (!lane) { showError("review: no reviewer lane in this task"); return; }
  var r = await api.createReview(agg.id, lane.id);
  // Failure returns 502 {review, error}; success / reuse returns the review.
  if (r.data && r.data.review) state.review = r.data.review;
  else if (r.data && typeof r.data === "object") state.review = r.data;
  if (fail(r, "run reviewer")) { renderReviewPanel(); renderInspector(); renderDrawer(); refreshEvents(); return; }
  clearError();
  renderReviewPanel(); renderInspector(); renderDrawer(); refreshEvents();
}

async function loadApprovals() {
  var r = await api.getLockboxRequests();
  if (r.ok && Array.isArray(r.data)) state.approvals = r.data;
}

async function loadPreview() {
  var r = await api.getApplyPreview();
  if (r.ok) state.applyPreview = r.data; else if (r.status === 404) state.applyPreview = null;
}

async function requestApproval() {
  var agg = currentAggregate();
  if (!agg) { showError("lockbox: aggregate proposals first"); return; }
  var r = await api.requestAggregateLockbox(agg.id);
  if (fail(r, "request approval")) return;
  clearError();
  await loadApprovals();
  renderReviewPanel(); renderDrawer(); refreshEvents();
}

async function decideApproval(decision) {
  var agg = currentAggregate();
  var approval = agg ? approvalForAggregate(agg.id) : null;
  if (!approval) { showError("lockbox: no approval request"); return; }
  var r = decision === "approve" ? await api.approveLockbox(approval.id) : await api.rejectLockbox(approval.id);
  if (fail(r, decision + " approval")) return;
  clearError();
  await loadApprovals();
  renderReviewPanel(); renderDrawer(); refreshEvents();
}

async function generatePreview() {
  var agg = currentAggregate();
  var approval = agg ? approvalForAggregate(agg.id) : null;
  if (!approval || approval.status !== "approved") { showError("preview: approve the Lockbox request first"); return; }
  var r = await api.createApplyPreview(approval.id);
  // ready/blocked/failed previews all carry an id (200/409/502); gate failures
  // return plain text instead.
  if (r.data && typeof r.data === "object" && r.data.id) {
    state.applyPreview = r.data; clearError();
  } else if (fail(r, "preview apply")) {
    renderReviewPanel(); refreshEvents(); return;
  }
  renderReviewPanel(); renderDrawer(); refreshEvents();
}

async function loadApply() {
  var r = await api.getApply();
  if (r.ok) state.apply = r.data; else if (r.status === 404) state.apply = null;
}

async function loadValidation() {
  var r = await api.getValidation();
  if (r.ok) state.validation = r.data; else if (r.status === 404) state.validation = null;
}

// applyApproved crosses the write boundary. It runs ONLY from this explicit
// click after a browser confirm() — never on load, preview, or approval.
async function applyApproved() {
  var ar = applyReadiness();
  if (!ar.canApply || !ar.approval) { showError("apply: " + (ar.hint || "not ready")); return; }
  if (!window.confirm("Apply will write the approved files to your project on disk. This crosses the write boundary and is not automatically undone. Continue?")) {
    return; // operator declined
  }
  var r = await api.applyApprovedPreview(ar.approval.id);
  if (r.data && r.data.id) {
    state.apply = r.data;
    if (r.data.status === "failed") showError("apply failed: " + (r.data.error || ""));
    else clearError();
  } else {
    showError("apply: " + (r.error || ("HTTP " + r.status)));
  }
  renderReviewPanel(); renderDrawer(); refreshEvents();
}

async function runValidation() {
  var vr = validationReadiness();
  if (!vr.applied) { showError("validation: apply first"); return; }
  var cmdEl = $("validate-command");
  var cmd = cmdEl ? cmdEl.value.trim() : "";
  var r = await api.runValidation(vr.applied.id, cmd);
  if (r.data && r.data.id) { state.validation = r.data; clearError(); }
  else if (fail(r, "run validation")) { renderReviewPanel(); refreshEvents(); return; }
  renderReviewPanel(); renderDrawer(); refreshEvents();
}

// ---- selection persistence + resume cues ----------------------------------
// Only lightweight, non-sensitive UI selection is persisted: the selected lane
// id and the console kind. Provider keys and conversation messages are never
// written to localStorage.
var SEL = { lane: "hf.selectedLaneId.v1", kind: "hf.selectedConsoleKind.v1" };

function lsGet(key) { try { return localStorage.getItem(key); } catch (e) { return null; } }
function lsSet(key, val) {
  try {
    if (val == null || val === "") localStorage.removeItem(key);
    else localStorage.setItem(key, String(val));
  } catch (e) { /* ignore (private mode / disabled storage) */ }
}

function persistSelection() {
  lsSet(SEL.kind, state.kind);
  lsSet(SEL.lane, state.selectedLane ? state.selectedLane.id : null);
}

// restoreSelection re-applies the persisted console kind / selected lane against
// freshly hydrated lanes, falling back safely when the saved lane is gone.
function restoreSelection() {
  var savedKind = lsGet(SEL.kind);
  var savedLaneId = lsGet(SEL.lane);

  // A still-existing saved lane is the strongest signal: restore it and adopt
  // its role as the console kind (how selecting a lane behaves normally).
  var lane = savedLaneId ? state.lanes.find(function (x) { return x.id === savedLaneId; }) : null;
  if (lane) {
    state.selectedLane = lane;
    state.kind = lane.role;
    return;
  }

  // No usable saved lane: restore the saved kind (if valid) with safe fallbacks.
  var kind = (savedKind && KINDS.indexOf(savedKind) >= 0) ? savedKind : state.kind;
  if (LANE_KINDS.indexOf(kind) >= 0) {
    var first = state.lanes.find(function (x) { return x.role === kind; });
    if (first) { state.kind = kind; state.selectedLane = first; }       // first lane of that role
    else if (!state.lanes.length) { state.kind = "architect"; state.selectedLane = null; } // no task/lanes
    else { state.kind = kind; state.selectedLane = null; }              // lanes exist but none of this role
  } else {
    state.kind = kind; state.selectedLane = null;                       // architect / lockbox / apply
  }
}

// showResumeCues surfaces a compact line describing what boot recovered, so the
// operator can see the thread was not lost across a refresh or restart.
function showResumeCues() {
  var items = [];
  if (state.project) items.push("Project restored");
  if (state.provider) items.push("Provider configured");
  if (state.architectSession) items.push("Architect session " + state.architectSession.status);
  if (state.cortexTask) items.push("Cortex task #" + state.cortexTask.id + " restored");
  if (state.selectedLane) items.push("Lane restored: " + state.selectedLane.role + " lane " + state.selectedLane.id);
  else if (state.laneConversation) items.push("Lane conversation #" + state.laneConversation.id + " restored");
  renderResume(items);
}

function renderResume(items) {
  var el = $("resume");
  if (!el) return;
  if (!items.length) { el.innerHTML = ""; el.classList.add("hidden"); return; }
  el.innerHTML = '<span class="resume-label">Resumed</span>' +
    items.map(function (x) { return '<span class="resume-pill">' + esc(x) + "</span>"; }).join("");
  el.classList.remove("hidden");
}

// ---- event polling --------------------------------------------------------
var EVENTS_POLL_MS = 5000;
var pollBusy = false;

// pollEvents refreshes the event stream on a timer, skipping while the tab is
// hidden and never piling up concurrent requests.
async function pollEvents() {
  if (document.hidden || pollBusy) return;
  pollBusy = true;
  try { await refreshEvents(); } finally { pollBusy = false; }
}

function startEventPolling() {
  setInterval(pollEvents, EVENTS_POLL_MS);
  // Refresh promptly when the operator returns to a previously hidden tab.
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden) pollEvents();
  });
}

// ---- boot -----------------------------------------------------------------
function wire() {
  $("btn-project-open").onclick = openProject;
  $("btn-provider-save").onclick = saveProvider;
  $("btn-provider-test").onclick = testProvider;
  $("btn-architect-start").onclick = startSession;
  $("btn-architect-send").onclick = sendArchitect;
  $("btn-architect-accept").onclick = acceptSpec;
  $("btn-architect-task").onclick = createTask;
  $("btn-conv-new").onclick = newConversation;
  $("btn-conv-send").onclick = sendConversation;
  $("btn-conv-close").onclick = closeConversation;
  $("btn-events-refresh").onclick = refreshEvents;

  // Context-panel switcher: each rail item switches the right panel. The
  // ticker's action jumps straight to the Log panel.
  var rail = $("navrail");
  if (rail) rail.addEventListener("click", function (e) {
    var b = e.target.closest("[data-view]");
    if (b) setView(b.getAttribute("data-view"));
  });
  var openLog = $("btn-open-log");
  if (openLog) openLog.onclick = function () { setView("log"); };
  // Next hint's optional jump button: switches the context panel (data-next-view)
  // or runs the frontend-only revise forward path (data-next-action). It never
  // triggers a backend action. Delegated because the hint re-renders.
  var nextHint = $("next-hint");
  if (nextHint) nextHint.addEventListener("click", function (e) {
    var v = e.target.closest("[data-next-view]");
    if (v) { setView(v.getAttribute("data-next-view")); return; }
    if (e.target.closest('[data-next-action="revise"]')) openBuilderRevision();
  });
  // Reviewer-revise forward path button (in the chat, near the composer).
  var revise = $("revise-banner");
  if (revise) revise.addEventListener("click", function (e) {
    if (e.target.closest('[data-action="open-revision"]')) openBuilderRevision();
  });
  // Central Current Step actions: the next valid workflow action, surfaced in the
  // chat. Each maps to the same handler reachable elsewhere — no hidden behavior.
  var stepEl = $("current-step");
  if (stepEl) stepEl.addEventListener("click", function (e) {
    var b = e.target.closest("[data-step-action]");
    if (!b) return;
    var a = b.getAttribute("data-step-action");
    if (a === "accept-spec") acceptSpec();
    else if (a === "create-task") createTask();
    else if (a === "select-builder") { var bl = firstBuilderLane(); if (bl) selectLane(bl.id); }
    else if (a === "gen-proposal") generateProposal();
    else if (a === "aggregate") aggregateProposals();
    else if (a === "review") runReviewer();
    else if (a === "revise") openBuilderRevision();
    else if (a === "send-conv") sendConversation();
    else if (a === "request-approval") requestApproval();
    else if (a === "approve") decideApproval("approve");
    else if (a === "reject") decideApproval("reject");
    else if (a === "preview") generatePreview();
    else if (a === "apply") applyApproved();
    else if (a === "validate") runValidation();
  });
  // "Edit setup" / "Hide setup": demote or reveal the setup controls in the rail.
  var setupToggle = $("btn-setup-toggle");
  if (setupToggle) setupToggle.onclick = toggleSetup;

  $("lanes").addEventListener("click", function (e) {
    var b = e.target.closest("[data-lane]");
    if (b) selectLane(b.getAttribute("data-lane"));
  });
  $("kind-chips").addEventListener("click", function (e) {
    var b = e.target.closest("[data-kind]");
    if (b) setKind(b.getAttribute("data-kind"));
  });
  // The lane artifact (gen-proposal) and the aggregate/lockbox/apply stage
  // panels all live in the right context panel; one delegated handler covers
  // every artifact action there.
  $("contextpanel").addEventListener("click", function (e) {
    if (e.target.closest('[data-action="gen-proposal"]')) generateProposal();
    else if (e.target.closest('[data-action="aggregate"]')) aggregateProposals();
    else if (e.target.closest('[data-action="review"]')) runReviewer();
    else if (e.target.closest('[data-action="request-approval"]')) requestApproval();
    else if (e.target.closest('[data-action="approve"]')) decideApproval("approve");
    else if (e.target.closest('[data-action="reject"]')) decideApproval("reject");
    else if (e.target.closest('[data-action="preview"]')) generatePreview();
    else if (e.target.closest('[data-action="apply"]')) applyApproved();
    else if (e.target.closest('[data-action="validate"]')) runValidation();
  });

  function onEnter(id, fn) {
    $(id).addEventListener("keydown", function (e) { if (e.key === "Enter") fn(); });
  }
  onEnter("project-path", openProject);
  onEnter("architect-goal", startSession);
  onEnter("architect-message", sendArchitect);
  onEnter("conv-message", sendConversation);

  // Live-enable Start / Send as the operator types into the goal / message.
  $("architect-goal").addEventListener("input", updateArchitectControls);
  $("architect-message").addEventListener("input", updateArchitectControls);
  $("conv-message").addEventListener("input", updateConsoleControls);
}

async function boot() {
  // Hydrate from the backend in parallel (fewer round-trips, less boot flicker).
  // A 404 / no-current-object is normal empty state, never an operator error; we
  // only ever set state from a successful read, so one optional 404 cannot wipe
  // usable state recovered from another endpoint.
  var r = await Promise.all([
    api.getProject(),
    api.getProvider(),
    api.getArchitectSession(),
    api.getArchitectSessions(),
    api.getCortexTask(),
    api.getLaneConversation(),
    api.getLaneConversations(),
    api.getEvents(),
    api.getLaneProposals(),
    api.getAggregate(),
    api.getReview(),
    api.getLockboxRequests(),
    api.getApplyPreview(),
    api.getApply(),
    api.getValidation(),
  ]);
  var proj = r[0], prov = r[1], sess = r[2], sessions = r[3],
    task = r[4], conv = r[5], convs = r[6], events = r[7], props = r[8], agg = r[9], review = r[10],
    approvals = r[11], preview = r[12], applyRes = r[13], validation = r[14];

  if (proj.ok) state.project = proj.data;
  if (prov.ok) state.provider = prov.data;
  if (sess.ok) setArchitectSession(sess.data);
  if (sessions.ok && Array.isArray(sessions.data)) state.sessionCount = sessions.data.length;
  if (task.ok) setCortexTask(task.data);
  if (conv.ok && conv.data) {
    state.laneConversation = conv.data;
    if (conv.data.context && conv.data.context.kind) state.kind = conv.data.context.kind;
  }
  if (convs.ok && Array.isArray(convs.data)) state.conversations = convs.data;
  if (events.ok && Array.isArray(events.data)) state.events = events.data;
  if (props.ok && Array.isArray(props.data)) state.proposals = props.data;
  if (agg.ok) state.aggregate = agg.data;
  if (review.ok) state.review = review.data;
  if (approvals.ok && Array.isArray(approvals.data)) state.approvals = approvals.data;
  if (preview.ok) state.applyPreview = preview.data;
  if (applyRes.ok) state.apply = applyRes.data;
  if (validation.ok) state.validation = validation.data;

  // Re-apply the operator's last selection against the hydrated lanes. Chat-first:
  // we do NOT auto-select a lane for a fresh task — the chat defaults to the
  // Architect until the operator clicks a lane.
  restoreSelection();
  // Show the conversation that matches the restored selection.
  syncConversationForContext();
  // Restore the operator's last context-panel view (frontend-only; default context).
  restoreView();
  // Restore the Setup-collapse preference (honored only once a task exists).
  restoreSetupCollapse();
  showResumeCues();

  wire();
  renderAll();
  startEventPolling();
}

document.addEventListener("DOMContentLoaded", boot);
