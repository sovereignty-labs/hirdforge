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
  events: [],
  error: "",
  loading: false,
  // supporting fields used by the UI
  sessionCount: 0,
  kind: "architect",        // active Lane Console context kind
  conversations: [],
};

var KINDS = ["architect", "builder", "reviewer", "validator", "lockbox", "apply"];
var LANE_KINDS = ["builder", "reviewer", "validator"];

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

// progressSteps describes the first operator loop, each step "done" once its
// state exists. The strip derives done/current/not-started from these.
function progressSteps() {
  return [
    { label: "Project", done: !!state.project },
    { label: "Provider", done: !!state.provider },
    { label: "Architect", done: !!state.architectSession },
    { label: "Cortex Task", done: !!state.cortexTask },
    { label: "Lane Console", done: state.conversations.length > 0 || !!state.laneConversation },
  ];
}

function renderProgress() {
  var el = $("progress");
  if (!el) return;
  var currentSet = false;
  el.innerHTML = progressSteps().map(function (s) {
    var cls = "step";
    if (s.done) cls += " done";
    else if (!currentSet) { cls += " current"; currentSet = true; } // first not-done = current
    else cls += " todo";
    return '<span class="' + cls + '">' + esc(s.label) + "</span>";
  }).join('<span class="step-sep">→</span>');
}

// renderNextHint gives the operator a single, obvious next action.
function renderNextHint() {
  var el = $("next-hint");
  if (!el) return;
  var msg;
  if (!state.project) msg = "Open a local project to begin.";
  else if (!state.provider) msg = "Configure a provider next.";
  else if (!state.architectSession) msg = "Start an Architect session.";
  else if (!state.cortexTask) msg = "Refine the spec, accept it, then create a Cortex task.";
  else if (!state.laneConversation) msg = "Select a lane and open the Lane Console.";
  else msg = "Setup complete — the operator loop is live.";
  el.textContent = "Next: " + msg;
}

// renderHeader keeps the chips, progress strip, and next hint in sync; call it
// wherever a setup-affecting state field changes.
function renderHeader() { renderChips(); renderProgress(); renderNextHint(); }

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
  else if (hasTask) r.hint = "Cortex task #" + architectTaskId() + " created — continue in the Lane Console.";
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
  mm.innerHTML = state.architectMessages.map(renderMsg).join("") || '<div class="empty">No messages yet.</div>';
  mm.scrollTop = mm.scrollHeight;

  $("architect-spec").innerHTML = (session && session.spec && session.spec.goal)
    ? renderSpec(session.spec, session.status === "accepted")
    : '<div class="empty">No structured spec yet.</div>';

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

// defaultLaneFor returns the lane to select by default: the first builder lane,
// else the first lane, else null.
function defaultLaneFor(lanes) {
  if (!lanes || !lanes.length) return null;
  return lanes.find(function (x) { return x.role === "builder"; }) || lanes[0];
}

// selectDefaultLane selects the default lane (used for a freshly created task).
function selectDefaultLane() {
  var l = defaultLaneFor(state.lanes);
  state.selectedLane = l || null;
  if (l) state.kind = l.role;
  syncConversationForContext();
  persistSelection();
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

// renderReviewPanel renders the task-level Aggregate & Review controls below the
// lane board.
function renderReviewPanel() {
  var el = $("review-panel");
  if (!el) return;
  if (!state.cortexTask) { el.innerHTML = ""; return; }
  var ar = aggregateReadiness();
  var rr = reviewReadiness();
  var html = '<div class="rev-head"><h2>Aggregate &amp; Review</h2></div>';
  html += '<div class="rev-actions">' +
    '<button class="rev-btn" data-action="aggregate"' + (ar.canAggregate ? "" : " disabled") + ">Aggregate Proposals</button>" +
    '<button class="rev-btn" data-action="review"' + (rr.canReview ? "" : " disabled") + ">Run Reviewer</button>" +
    "</div>";
  html += '<div class="hint">' + esc(ar.agg ? rr.hint : ar.hint) + "</div>";
  html += ar.agg ? renderAggregate(ar.agg) : '<div class="empty">No aggregate yet.</div>';
  if (rr.review) html += renderReview(rr.review);
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
      : '<div class="ins-row"><b>conversation</b> <span class="muted">none — start one in the Lane Console</span></div>');

  if (l.role === "builder") {
    var pr = proposalReadiness();
    rows += '<div class="ins-prop">' +
      '<div class="ins-prop-head"><b>builder proposal</b>' +
      '<button class="prop-btn" data-action="gen-proposal"' + (pr.canGenerate ? "" : " disabled") + ">Generate Builder Proposal</button></div>" +
      (pr.existing ? renderProposal(pr.existing) : '<div class="empty">No proposal yet.</div>') +
      (pr.hint ? '<div class="hint">' + esc(pr.hint) + "</div>" : "") +
      "</div>";
  }

  if (l.role === "reviewer") {
    var rr = reviewReadiness();
    rows += '<div class="ins-prop"><div class="ins-prop-head"><b>reviewer</b></div>' +
      (rr.review ? renderReview(rr.review) : '<div class="empty">No review yet. Aggregate proposals, then Run Reviewer.</div>') +
      "</div>";
  }
  b.innerHTML = rows;
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
  var evs = state.events.slice().reverse(); // newest first
  list.innerHTML = evs.map(function (e) {
    return '<div class="event"><span class="ev-type">' + esc(e.type) + "</span>" +
      '<span class="ev-msg">' + esc(e.message || "") + "</span>" +
      '<span class="ev-ts">' + esc(fmtTs(e.ts)) + "</span></div>";
  }).join("") || '<div class="empty">No events.</div>';
}

function renderAll() {
  renderHeader(); renderProject(); renderProvider(); renderArchitect();
  renderBoard(); renderReviewPanel(); renderInspector(); renderDrawer(); renderEvents();
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
  clearError(); setCortexTask(r.data); selectDefaultLane(); // auto-select the first builder lane
  // Record the task on the session locally so the panel reflects it without a
  // reload (the backend already persisted cortex_task_id).
  if (state.architectSession && r.data) state.architectSession.cortex_task_id = r.data.id;
  renderBoard(); renderInspector(); renderDrawer(); renderArchitect(); renderHeader(); refreshEvents();
}

function selectLane(id) {
  var l = state.lanes.find(function (x) { return x.id === id; });
  if (!l) return;
  state.selectedLane = l; state.kind = l.role;
  syncConversationForContext(); // show this lane's conversation if one exists
  persistSelection();
  renderBoard(); renderInspector(); renderDrawer(); renderHeader();
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
  renderBoard(); renderInspector(); renderDrawer(); renderHeader();
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

// ---- Lane Console resize --------------------------------------------------
var CONSOLE = { key: "hf.consoleHeight.v1", def: 320, min: 180, max: 560 };

function clampConsole(h) { return Math.max(CONSOLE.min, Math.min(CONSOLE.max, h)); }

function currentConsoleHeight() {
  var v = parseInt(getComputedStyle(document.documentElement).getPropertyValue("--console-h"), 10);
  return isNaN(v) ? CONSOLE.def : v;
}

function setConsoleHeight(h, persist) {
  h = clampConsole(Math.round(h));
  document.documentElement.style.setProperty("--console-h", h + "px");
  if (persist) { try { localStorage.setItem(CONSOLE.key, String(h)); } catch (e) { /* ignore */ } }
}

function loadConsoleHeight() {
  var h = CONSOLE.def;
  try { var v = parseInt(localStorage.getItem(CONSOLE.key), 10); if (!isNaN(v)) h = v; } catch (e) { /* ignore */ }
  setConsoleHeight(h, false);
}

function initConsoleResize() {
  loadConsoleHeight();
  var grip = $("drawer-grip");
  if (!grip) return;
  var dragging = false, startY = 0, startH = 0;

  function onMove(e) {
    if (!dragging) return;
    var y = (e.touches && e.touches[0]) ? e.touches[0].clientY : e.clientY;
    // Dragging the grip upward (smaller clientY) grows the console; the extra
    // height is taken from the board area above, never from the event stream.
    setConsoleHeight(startH + (startY - y), false);
    e.preventDefault();
  }
  function onUp() {
    if (!dragging) return;
    dragging = false;
    document.body.style.userSelect = "";
    setConsoleHeight(currentConsoleHeight(), true); // persist final height
  }
  function onDown(e) {
    dragging = true;
    startY = (e.touches && e.touches[0]) ? e.touches[0].clientY : e.clientY;
    startH = currentConsoleHeight();
    document.body.style.userSelect = "none";
    e.preventDefault();
  }

  grip.addEventListener("mousedown", onDown);
  grip.addEventListener("touchstart", onDown, { passive: false });
  window.addEventListener("mousemove", onMove);
  window.addEventListener("touchmove", onMove, { passive: false });
  window.addEventListener("mouseup", onUp);
  window.addEventListener("touchend", onUp);
  // Double-click the grip to reset to the default height.
  grip.addEventListener("dblclick", function () { setConsoleHeight(CONSOLE.def, true); });
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
  if (state.selectedLane) items.push("Lane Console restored: " + state.selectedLane.role + " lane " + state.selectedLane.id);
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

  $("lanes").addEventListener("click", function (e) {
    var b = e.target.closest("[data-lane]");
    if (b) selectLane(b.getAttribute("data-lane"));
  });
  $("kind-chips").addEventListener("click", function (e) {
    var b = e.target.closest("[data-kind]");
    if (b) setKind(b.getAttribute("data-kind"));
  });
  $("inspector-body").addEventListener("click", function (e) {
    if (e.target.closest('[data-action="gen-proposal"]')) generateProposal();
  });
  $("review-panel").addEventListener("click", function (e) {
    if (e.target.closest('[data-action="aggregate"]')) aggregateProposals();
    else if (e.target.closest('[data-action="review"]')) runReviewer();
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

  initConsoleResize();
}

async function boot() {
  loadConsoleHeight(); // apply the persisted drawer height before the first paint

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
  ]);
  var proj = r[0], prov = r[1], sess = r[2], sessions = r[3],
    task = r[4], conv = r[5], convs = r[6], events = r[7], props = r[8], agg = r[9], review = r[10];

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

  // Re-apply the operator's last selection against the hydrated lanes.
  restoreSelection();
  // For a fresh task with no prior saved lane, default-select a lane (first
  // builder, else first). This never overrides a valid restored selection.
  if (!state.selectedLane && state.lanes.length && !lsGet(SEL.lane)) {
    selectDefaultLane();
  }
  // Show the conversation that matches the restored / default selection.
  syncConversationForContext();
  showResumeCues();

  wire();
  renderAll();
  startEventPolling();
}

document.addEventListener("DOMContentLoaded", boot);
