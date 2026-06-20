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
  var s = "";
  if (state.provider) {
    s += "<div>model <b>" + esc(state.provider.model) + "</b> · key " + (state.provider.api_key_set ? "set" : "unset") + "</div>";
    s += '<div class="mono">' + esc(state.provider.base_url) + "</div>";
  } else {
    s = "No provider configured.";
  }
  if (state.providerTest) {
    var t = state.providerTest;
    s += '<div class="' + (t.ok ? "ok" : "bad") + '">test: ' +
      (t.ok ? "ok (" + esc(t.model || "") + ")" : "failed — " + esc(t.error || t.status)) + "</div>";
  }
  m.innerHTML = s;
}

function renderMsg(m) {
  return '<div class="msg msg-' + esc(m.role) + '"><span class="role">' + esc(m.role) +
    '</span><span class="content">' + esc(m.content) + "</span></div>";
}

function renderSpec(spec) {
  function list(label, arr) {
    if (!arr || !arr.length) return "";
    return '<div class="spec-row"><b>' + label + "</b><ul>" +
      arr.map(function (x) { return "<li>" + esc(x) + "</li>"; }).join("") + "</ul></div>";
  }
  return '<div class="spec-goal"><b>goal</b> ' + esc(spec.goal) + "</div>" +
    list("constraints", spec.constraints) +
    list("affected areas", spec.affected_areas) +
    list("acceptance", spec.acceptance_criteria) +
    list("risks", spec.risks) +
    list("open questions", spec.open_questions) +
    list("suggested lanes", spec.suggested_lanes);
}

function renderArchitect() {
  var session = state.architectSession;
  var meta = $("architect-meta");
  if (!session) {
    meta.textContent = state.sessionCount ? ("No active session (" + state.sessionCount + " total).") : "No session.";
  } else {
    meta.innerHTML = "session <b>" + esc(session.id) + "</b> · " +
      '<span class="status-' + esc(session.status) + '">' + esc(session.status) + "</span>" +
      " · " + state.sessionCount + " total";
  }
  var mm = $("architect-messages");
  mm.innerHTML = state.architectMessages.map(renderMsg).join("") || '<div class="empty">No messages.</div>';
  mm.scrollTop = mm.scrollHeight;

  $("architect-spec").innerHTML = (session && session.spec) ? renderSpec(session.spec) : "";

  var active = !!(session && session.status === "active");
  var accepted = !!(session && session.status === "accepted");
  $("btn-architect-send").disabled = !active;
  $("btn-architect-accept").disabled = !active;
  $("btn-architect-task").disabled = !accepted;
}

function renderBoard() {
  var meta = $("board-meta");
  var wrap = $("lanes");
  if (!state.cortexTask) {
    meta.textContent = "No Cortex task. Accept an Architect spec, then create a task.";
    wrap.innerHTML = "";
    return;
  }
  meta.innerHTML = "task <b>#" + esc(state.cortexTask.id) + "</b> · " + esc(state.cortexTask.status) + " · " + esc(state.cortexTask.goal);
  wrap.innerHTML = state.lanes.map(function (l) {
    var sel = state.selectedLane && state.selectedLane.id === l.id ? " selected" : "";
    return '<button class="lane role-' + esc(l.role) + sel + '" data-lane="' + esc(l.id) + '">' +
      '<div class="lane-role">' + esc(l.role) + " " + l.index + "</div>" +
      '<div class="lane-task">' + esc(l.task) + "</div>" +
      '<div class="lane-status">' + esc(l.status) + "</div>" +
      "</button>";
  }).join("") || '<div class="empty">No lanes.</div>';
}

function renderInspector() {
  var b = $("inspector-body");
  if (state.selectedLane) {
    var l = state.selectedLane;
    b.innerHTML =
      '<div class="ins-row"><b>lane</b> ' + esc(l.id) + "</div>" +
      '<div class="ins-row"><b>role</b> ' + esc(l.role) + "</div>" +
      '<div class="ins-row"><b>index</b> ' + esc(l.index) + "</div>" +
      '<div class="ins-row"><b>status</b> ' + esc(l.status) + "</div>" +
      '<div class="ins-row"><b>task</b> ' + esc(l.task) + "</div>" +
      (l.workspace_path ? '<div class="ins-row"><b>workspace</b> <span class="mono">' + esc(l.workspace_path) + "</span></div>" : "") +
      (l.worktree_branch ? '<div class="ins-row"><b>branch</b> ' + esc(l.worktree_branch) + "</div>" : "");
  } else {
    b.innerHTML = '<div class="ins-row"><b>context</b> ' + esc(state.kind) + "</div>" +
      '<div class="empty">No lane selected. Pick a lane on the board, or a context chip below.</div>';
  }
}

function renderDrawer() {
  $("kind-chips").innerHTML = KINDS.map(function (k) {
    return '<button class="kind' + (k === state.kind ? " active" : "") + '" data-kind="' + k + '">' + k + "</button>";
  }).join("");

  var scope = ["kind <b>" + esc(state.kind) + "</b>"];
  if (state.selectedLane && LANE_KINDS.indexOf(state.kind) >= 0) {
    scope.push("lane " + esc(state.selectedLane.id) + " (" + esc(state.selectedLane.role) + ")");
  }
  if (state.laneConversation) {
    scope.push("conversation #" + esc(state.laneConversation.id) + " · " +
      '<span class="status-' + esc(state.laneConversation.status) + '">' + esc(state.laneConversation.status) + "</span>");
  }
  $("drawer-scope").innerHTML = scope.join(" · ");

  var msgs = (state.laneConversation && state.laneConversation.messages) || [];
  var cm = $("conv-messages");
  cm.innerHTML = msgs.map(renderMsg).join("") ||
    '<div class="empty">No conversation. Start one for the selected lane / context.</div>';
  cm.scrollTop = cm.scrollHeight;

  var closed = !!(state.laneConversation && state.laneConversation.status === "closed");
  $("btn-conv-send").disabled = !state.laneConversation || closed;
  $("btn-conv-close").disabled = !state.laneConversation || closed;
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
  renderChips(); renderProject(); renderProvider(); renderArchitect();
  renderBoard(); renderInspector(); renderDrawer(); renderEvents();
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
  clearError(); state.project = r.data; renderProject(); renderChips(); refreshEvents();
}

async function saveProvider() {
  var config = {
    base_url: $("provider-base").value.trim(),
    api_key: $("provider-key").value,
    model: $("provider-model").value.trim(),
  };
  var r = await api.saveProvider(config);
  if (fail(r, "save provider")) return;
  clearError(); state.provider = r.data; state.providerTest = null; renderProvider(); renderChips(); refreshEvents();
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
  clearError(); setArchitectSession(r.data); await loadSessions(); renderArchitect(); renderChips(); refreshEvents();
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
  var r = await api.createCortexTask(state.architectSession.id);
  if (fail(r, "create cortex task")) return;
  clearError(); setCortexTask(r.data); state.selectedLane = null; persistSelection();
  renderBoard(); renderInspector(); renderChips(); refreshEvents();
}

function selectLane(id) {
  var l = state.lanes.find(function (x) { return x.id === id; });
  if (!l) return;
  state.selectedLane = l; state.kind = l.role;
  persistSelection();
  renderBoard(); renderInspector(); renderDrawer();
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
  persistSelection();
  renderBoard(); renderInspector(); renderDrawer();
}

async function newConversation() {
  var body = { kind: state.kind };
  if (LANE_KINDS.indexOf(state.kind) >= 0) {
    if (!state.selectedLane) { showError("lane console: select a " + state.kind + " lane first"); return; }
    body.lane_id = state.selectedLane.id;
    if (state.cortexTask) body.task_id = state.cortexTask.id;
  }
  var r = await api.createLaneConversation(body);
  if (fail(r, "new conversation")) return;
  clearError(); state.laneConversation = r.data; renderDrawer(); refreshEvents(); loadConversations();
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
  clearError(); state.laneConversation = r.data; renderDrawer(); refreshEvents(); loadConversations();
}

async function loadConversations() {
  var r = await api.getLaneConversations();
  if (r.ok && Array.isArray(r.data)) state.conversations = r.data;
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

  function onEnter(id, fn) {
    $(id).addEventListener("keydown", function (e) { if (e.key === "Enter") fn(); });
  }
  onEnter("project-path", openProject);
  onEnter("architect-goal", startSession);
  onEnter("architect-message", sendArchitect);
  onEnter("conv-message", sendConversation);

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
  ]);
  var proj = r[0], prov = r[1], sess = r[2], sessions = r[3],
    task = r[4], conv = r[5], convs = r[6], events = r[7];

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

  // Re-apply the operator's last selection against the hydrated lanes, then
  // announce what was recovered.
  restoreSelection();
  showResumeCues();

  wire();
  renderAll();
  startEventPolling();
}

document.addEventListener("DOMContentLoaded", boot);
