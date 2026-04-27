// Hirdforge UI 1.0 — Visual Strawman v0.9
//
// This file is the visual/layout reference for UI 1.0. It is NOT compiled or
// shipped — it is a reference document that lives alongside `docs/ui/spec.md`.
// Builders implementing the UI consume this file as code-shaped pseudocode for
// layout structure, color tokens, dimensions, and visual vocabulary.
//
// Locked at S119. Amendments land via PR against this file.
//
// Stack assumptions (illustrative, not normative): React, Tailwind CSS,
// lucide-react icons. The actual implementation in `cmd/gateway/` may differ
// in stack — what matters is fidelity to the layout shape.

import React, { useState, useEffect } from 'react';
import {
  MessageSquare, Users, Server, Send, GitBranch, FileCode, Activity,
  CheckCircle2, GitCommit, GitPullRequest, FolderGit2, Circle,
  Hand, Pause, Folder, FolderOpen, File, ChevronRight, ChevronDown,
  Mail, ShieldAlert, Edit3, Calendar, Inbox, Clock
} from 'lucide-react';

const HirdforgeUI = () => {
  const [activeAgent, setActiveAgent] = useState('jeeves');
  const [activeTab, setActiveTab] = useState('comms');
  const [cursorBlink, setCursorBlink] = useState(true);
  const [leftRailWidth, setLeftRailWidth] = useState(260);
  const [activityHeight, setActivityHeight] = useState(176);
  const [isDraggingTree, setIsDraggingTree] = useState(false);
  const [isDraggingActivity, setIsDraggingActivity] = useState(false);

  useEffect(() => {
    const id = setInterval(() => setCursorBlink(b => !b), 530);
    return () => clearInterval(id);
  }, []);

  const startDragTree = (e) => {
    e.preventDefault();
    setIsDraggingTree(true);
    const startX = e.clientX;
    const startWidth = leftRailWidth;
    const onMove = (ev) => {
      const next = Math.max(160, Math.min(500, startWidth + (ev.clientX - startX)));
      setLeftRailWidth(next);
    };
    const onUp = () => {
      setIsDraggingTree(false);
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
    };
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
  };

  const startDragActivity = (e) => {
    e.preventDefault();
    setIsDraggingActivity(true);
    const startY = e.clientY;
    const startHeight = activityHeight;
    const onMove = (ev) => {
      const next = Math.max(60, Math.min(400, startHeight - (ev.clientY - startY)));
      setActivityHeight(next);
    };
    const onUp = () => {
      setIsDraggingActivity(false);
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
    };
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
  };

  // NOTE: agent identifiers below are illustrative for visual reference only.
  // Per spec §9, real surface selection is activity-driven, not name-driven.
  const agents = [
    { id: 'ragnar',  name: 'Ragnar',  role: 'Architect', model: 'MiniMax M2.7', status: 'active', task: 'Coordinating #211 review' },
    { id: 'ivar',    name: 'Ivar',    role: 'Builder',   model: 'Gemma 4 26B (local-builder)', status: 'active', task: 'Writing rbac/gateway-argocd.yaml' },
    { id: 'jeeves',  name: 'Jeeves',  role: 'PA',        model: 'MiniMax M2.7', status: 'active', task: 'Drafting reply to Sarah Chen', pendingApproval: true, flag: true },
    { id: 'sindri',  name: 'Sindri',  role: 'Builder',   model: 'MiniMax M2.7', status: 'active', task: 'Reading PR #210 diff' },
    { id: 'chuck',   name: 'Chuck',   role: 'Builder',   model: 'MiniMax M2.7', status: 'idle' },
    { id: 'val',     name: 'Val',     role: 'Builder',   model: 'MiniMax M2.7', status: 'idle' },
    { id: 'leif',    name: 'Leif',    role: 'Builder',   model: 'MiniMax M2.7', status: 'idle' },
    { id: 'freya',   name: 'Freya',   role: 'Reviewer',  model: 'GPT-5.2',      status: 'idle' },
    { id: 'thane',   name: 'Thane',   role: 'Reviewer',  model: 'MiniMax M2.7', status: 'idle' },
  ];

  const focused = agents.find(a => a.id === activeAgent);
  const pendingCount = agents.filter(a => a.pendingApproval).length;

  const ivarActivity = [
    { t: '2m ago',  text: 'cloned kit/asgard-infra',                icon: FolderGit2 },
    { t: '90s ago', text: 'created branch ivar/argocd-rbac-fix',    icon: GitBranch },
    { t: '70s ago', text: 'read rbac/agent-roles.yaml',             icon: FileCode },
    { t: '45s ago', text: 'read rbac/asgard-agent-binding.yaml',    icon: FileCode },
    { t: 'now',     text: 'writing rbac/gateway-argocd.yaml',       icon: FileCode, current: true },
    { t: 'next',    text: 'validate yaml',                          icon: CheckCircle2, pending: true },
    { t: 'next',    text: 'commit and push',                        icon: GitCommit, pending: true },
    { t: 'next',    text: 'open PR',                                icon: GitPullRequest, pending: true },
  ];

  const jeevesActivity = [
    { t: '4m ago',  text: 'received email from Sarah Chen',          icon: Inbox },
    { t: '3m ago',  text: 'classified as response-needed',           icon: Mail },
    { t: '2m ago',  text: 'checked calendar — Fri 2pm available',    icon: Calendar },
    { t: '90s ago', text: 'composed reply',                          icon: Edit3 },
    { t: 'now',     text: 'queued send for Sovereign approval',      icon: ShieldAlert, current: true },
    { t: 'next',    text: 'send email',                              icon: Send,     pending: true },
    { t: 'next',    text: 'send calendar invite',                    icon: Calendar, pending: true },
  ];

  const tree = {
    name: 'kit/asgard-infra',
    type: 'repo',
    children: [
      { name: 'infrastructure', type: 'folder', collapsed: true },
      {
        name: 'rbac', type: 'folder', expanded: true,
        children: [
          { name: 'agent-roles.yaml',          type: 'file', state: 'read',    age: '70s' },
          { name: 'asgard-agent-binding.yaml', type: 'file', state: 'read',    age: '45s' },
          { name: 'gateway-argocd.yaml',       type: 'file', state: 'writing', age: 'now' },
        ]
      },
      { name: 'networkpolicies', type: 'folder', collapsed: true },
      { name: 'sealed-secrets',  type: 'folder', collapsed: true },
      { name: 'README.md',       type: 'file',   state: 'clean' },
    ]
  };

  const yamlLines = [
    { n: 1,  c: 'apiVersion: rbac.authorization.k8s.io/v1' },
    { n: 2,  c: 'kind: ClusterRole' },
    { n: 3,  c: 'metadata:' },
    { n: 4,  c: '  name: gateway-argocd-reader' },
    { n: 5,  c: '  labels:' },
    { n: 6,  c: '    asgard.io/component: gateway' },
    { n: 7,  c: 'rules:' },
    { n: 8,  c: '  - apiGroups: ["argoproj.io"]' },
    { n: 9,  c: '    resources: ["applications"]' },
    { n: 10, c: '    verbs: ["get", "list", "watch", "patch"]', writing: true },
  ];

  const StatusDot = ({ status, flag }) => (
    <span className="relative inline-flex items-center justify-center">
      <span className={`w-2 h-2 rounded-full ${status === 'active' ? 'bg-emerald-400' : 'bg-zinc-600'}`} />
      {status === 'active' && (
        <span className="absolute w-2 h-2 rounded-full bg-emerald-400 animate-ping opacity-75" />
      )}
      {flag && (
        <span className="absolute -top-1 -right-1 w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />
      )}
    </span>
  );

  const TreeNode = ({ node, depth = 0 }) => {
    const indent = { paddingLeft: `${depth * 12 + 8}px` };
    if (node.type === 'repo') {
      return (
        <div>
          <div className="flex items-center gap-1.5 px-2 py-1 text-xs text-zinc-400 border-b border-zinc-800/60">
            <FolderGit2 className="w-3 h-3 text-amber-400 shrink-0" />
            <span className="text-zinc-300 truncate">{node.name}</span>
            <span className="ml-auto text-[10px] text-zinc-600 flex items-center gap-1 shrink-0">
              <GitBranch className="w-2.5 h-2.5" /> ivar/argocd-rbac-fix
            </span>
          </div>
          {node.children?.map((c, i) => <TreeNode key={i} node={c} depth={0} />)}
        </div>
      );
    }
    if (node.type === 'folder') {
      const Icon = node.expanded ? FolderOpen : Folder;
      const Chev = node.expanded ? ChevronDown : ChevronRight;
      return (
        <div>
          <div style={indent} className="flex items-center gap-1 py-0.5 text-xs text-zinc-500 hover:text-zinc-300 cursor-pointer">
            <Chev className="w-3 h-3 shrink-0" />
            <Icon className="w-3 h-3 shrink-0" />
            <span className="truncate">{node.name}</span>
          </div>
          {node.expanded && node.children?.map((c, i) => <TreeNode key={i} node={c} depth={depth + 1} />)}
        </div>
      );
    }
    const stateClasses =
      node.state === 'writing' ? 'bg-amber-500/10 text-amber-200 border-l-2 border-amber-500'
      : node.state === 'read'  ? 'text-zinc-300'
      : 'text-zinc-500';
    const dotClass =
      node.state === 'writing' ? 'bg-amber-400 animate-pulse'
      : node.state === 'read'  ? 'bg-blue-400/60'
      : 'bg-transparent';
    return (
      <div style={indent} className={`flex items-center gap-1.5 py-0.5 text-xs cursor-pointer ${stateClasses}`}>
        <span className="w-3 shrink-0" />
        <File className="w-3 h-3 opacity-70 shrink-0" />
        <span className="flex-1 truncate">{node.name}</span>
        {node.state !== 'clean' && (
          <>
            <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${dotClass}`} />
            <span className="text-[9px] text-zinc-600 w-7 text-right shrink-0">{node.age}</span>
          </>
        )}
      </div>
    );
  };

  const dragCursorClass =
    isDraggingTree     ? 'cursor-col-resize select-none' :
    isDraggingActivity ? 'cursor-row-resize select-none' : '';

  // Reusable activity timeline — used by every surface type
  const ActivityTimeline = ({ items }) => (
    <>
      <div onMouseDown={startDragActivity}
        className={`h-1 shrink-0 cursor-row-resize transition-colors ${
          isDraggingActivity ? 'bg-zinc-700' : 'bg-transparent hover:bg-zinc-800'
        }`} />
      <div style={{ height: `${activityHeight}px` }} className="border-t border-zinc-800 bg-zinc-900/50 shrink-0 flex flex-col">
        <div className="px-3 py-1.5 text-[10px] uppercase tracking-wider text-zinc-500 flex items-center gap-2 border-b border-zinc-800 shrink-0">
          <Activity className="w-3 h-3" />
          <span>Activity</span>
          <span className="ml-auto text-zinc-600">{items.length} events</span>
        </div>
        <div className="flex-1 overflow-y-auto px-3 py-2 space-y-1">
          {items.map((item, i) => {
            const Icon = item.icon;
            const textColor = item.current ? 'text-amber-300' : item.pending ? 'text-zinc-600' : 'text-zinc-400';
            const iconColor = item.current ? 'text-amber-400' : item.pending ? 'text-zinc-700' : 'text-zinc-500';
            return (
              <div key={i} className={`flex items-center gap-2 text-[11px] ${textColor}`}>
                <span className="text-[9px] text-zinc-600 w-12 shrink-0">{item.t}</span>
                <Icon className={`w-3 h-3 shrink-0 ${iconColor}`} />
                <span className="truncate">{item.text}</span>
                {item.current && <span className="w-1 h-1 rounded-full bg-amber-400 animate-pulse shrink-0" />}
              </div>
            );
          })}
        </div>
      </div>
    </>
  );

  const LeftRailHandle = () => (
    <div onMouseDown={startDragTree}
      className={`w-1 shrink-0 cursor-col-resize transition-colors ${
        isDraggingTree ? 'bg-zinc-700' : 'bg-transparent hover:bg-zinc-800'
      }`} />
  );

  return (
    <div className={`h-screen w-screen bg-zinc-950 text-zinc-100 flex flex-col overflow-hidden font-mono text-xs ${dragCursorClass}`}>
      <style>{`
        ::-webkit-scrollbar { width: 8px; height: 8px; }
        ::-webkit-scrollbar-track { background: transparent; }
        ::-webkit-scrollbar-thumb { background: rgb(39 39 42); border-radius: 4px; }
        ::-webkit-scrollbar-thumb:hover { background: rgb(63 63 70); }
        * { scrollbar-width: thin; scrollbar-color: rgb(39 39 42) transparent; }
      `}</style>

      {/* TOP BAR */}
      <div className="h-10 border-b border-zinc-800 bg-zinc-900 flex items-center px-3 gap-3 shrink-0">
        <div className="flex items-center gap-1.5">
          <div className="w-4 h-4 bg-amber-500 rounded-sm flex items-center justify-center">
            <span className="text-zinc-950 font-bold text-[10px]">H</span>
          </div>
          <span className="font-bold tracking-wider text-amber-400 text-xs">HIRDFORGE</span>
        </div>

        <div className="ml-2 flex items-center gap-0">
          {[
            { id: 'comms',    label: 'Comms',    icon: MessageSquare },
            { id: 'warriors', label: 'Warriors', icon: Users },
            { id: 'realm',    label: 'Realm',    icon: Server },
          ].map(t => {
            const Icon = t.icon;
            const isActive = activeTab === t.id;
            return (
              <button key={t.id} onClick={() => setActiveTab(t.id)}
                className={`flex items-center gap-1.5 px-3 py-1 text-xs transition-colors ${
                  isActive ? 'text-amber-400 border-b border-amber-400 -mb-px' : 'text-zinc-500 hover:text-zinc-300'
                }`}>
                <Icon className="w-3 h-3" />
                <span>{t.label}</span>
              </button>
            );
          })}
        </div>

        <div className="ml-auto flex items-center gap-3">
          {pendingCount > 0 && (
            <button className="relative flex items-center gap-1 text-amber-400 hover:text-amber-300">
              <ShieldAlert className="w-4 h-4" />
              <span className="text-[10px] font-semibold">{pendingCount}</span>
              <span className="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />
            </button>
          )}
          <span className="text-zinc-500 text-[10px]">kit@hirdforge</span>
        </div>
      </div>

      {/* MAIN BODY: 50/50 split */}
      <div className="flex-1 flex min-h-0">

        {/* ========== LEFT HALF — TAB CONTENT ========== */}
        <div className="w-1/2 border-r border-zinc-800 flex flex-col min-h-0">
          {activeTab === 'comms' && (
            <div className="flex-1 flex min-h-0">
              {/* warband rail */}
              <div className="w-44 border-r border-zinc-800 bg-zinc-900/30 overflow-y-auto shrink-0">
                <div className="px-2 py-1.5 text-[10px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800/60">
                  Warband · {agents.length}
                </div>
                {agents.map(a => (
                  <button key={a.id} onClick={() => setActiveAgent(a.id)}
                    className={`w-full flex items-center gap-2 px-2 py-1.5 text-left transition-colors border-l-2 ${
                      activeAgent === a.id
                        ? 'bg-zinc-900 border-amber-500'
                        : 'border-transparent hover:bg-zinc-900/50'
                    }`}>
                    <StatusDot status={a.status} flag={a.flag} />
                    <div className="flex-1 min-w-0">
                      <div className={`text-xs truncate ${activeAgent === a.id ? 'text-zinc-100' : 'text-zinc-400'}`}>
                        {a.name}
                      </div>
                      <div className="text-[9px] text-zinc-600 truncate">{a.role}</div>
                    </div>
                  </button>
                ))}
              </div>

              {/* chat pane */}
              <div className="flex-1 flex flex-col min-w-0">
                <div className="px-3 py-1.5 border-b border-zinc-800 bg-zinc-900/30 flex items-center gap-2 shrink-0">
                  <StatusDot status={focused.status} />
                  <span className="text-zinc-300 text-xs">{focused.name}</span>
                  <span className="text-zinc-600 text-[10px]">·</span>
                  <span className="text-zinc-500 text-[10px]">{focused.role}</span>
                  <span className="text-zinc-600 text-[10px] ml-auto truncate">{focused.model}</span>
                </div>

                <div className="flex-1 overflow-y-auto px-3 py-2 space-y-3 text-[11px]">
                  <Message who="kit" name="Kit" time="14:01">
                    Take a look at PR #211, the gateway argocd RBAC fix.
                  </Message>
                  <Message who="agent" name={focused.name} time="14:02">
                    Sure — pulling the diff now. Initial scan shows two new manifests under <code className="text-amber-300">rbac/</code>: a ClusterRole and a ClusterRoleBinding for the gateway service account…
                  </Message>
                  <Message who="agent" name={focused.name} time="14:03">
                    <div className="flex items-center gap-1.5 text-[10px] text-zinc-500 mb-1">
                      <FileCode className="w-2.5 h-2.5" />
                      <span>tool: gitea_file_read</span>
                      <span className="text-zinc-600">→ rbac/gateway-argocd.yaml</span>
                    </div>
                    Verbs look right: get, list, watch, patch on argoproj.io/applications. Binding subject matches the gateway SA. I'd approve.
                  </Message>
                </div>

                <div className="border-t border-zinc-800 bg-zinc-900/50 px-3 py-2 shrink-0">
                  <div className="flex items-end gap-2">
                    <textarea rows={1}
                      placeholder={`Message ${focused.name}…`}
                      className="flex-1 bg-zinc-950 border border-zinc-800 rounded px-2 py-1.5 text-xs text-zinc-100 placeholder-zinc-600 focus:outline-none focus:border-amber-500 resize-none" />
                    <button className="bg-amber-500 hover:bg-amber-400 text-zinc-950 rounded p-1.5">
                      <Send className="w-3.5 h-3.5" />
                    </button>
                  </div>
                  <div className="mt-1 text-[9px] text-zinc-600 flex items-center justify-between">
                    <span>Enter to send · Shift+Enter for newline</span>
                    <span>{focused.status === 'active' ? '● ready' : '○ idle'}</span>
                  </div>
                </div>
              </div>
            </div>
          )}

          {activeTab === 'warriors' && (
            <div className="flex-1 overflow-y-auto p-3">
              <div className="text-[10px] uppercase tracking-wider text-zinc-500 mb-2">Fleet · {agents.length}</div>
              <div className="grid grid-cols-2 gap-2">
                {agents.map(a => (
                  <div key={a.id} className="border border-zinc-800 rounded p-2 bg-zinc-900/30">
                    <div className="flex items-center gap-1.5 mb-1">
                      <StatusDot status={a.status} />
                      <span className="text-zinc-200 text-xs font-semibold">{a.name}</span>
                      <span className="text-[9px] text-zinc-600 ml-auto">{a.role}</span>
                    </div>
                    <div className="text-[10px] text-zinc-500 truncate mb-1">{a.model}</div>
                    {a.task && <div className="text-[10px] text-zinc-400 truncate">{a.task}</div>}
                    <div className="mt-2 flex gap-1">
                      <button className="flex-1 text-[9px] py-0.5 border border-zinc-800 rounded text-zinc-500 hover:text-zinc-300 hover:border-zinc-700">Pause</button>
                      <button className="flex-1 text-[9px] py-0.5 border border-zinc-800 rounded text-zinc-500 hover:text-zinc-300 hover:border-zinc-700">Inject</button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {activeTab === 'realm' && (
            <div className="flex-1 overflow-y-auto p-3 space-y-3">
              <div className="border border-zinc-800 rounded">
                <div className="px-2 py-1 text-[10px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800 flex items-center gap-2">
                  <Server className="w-3 h-3" /> Cluster · asgard
                </div>
                <div className="px-2 py-1.5 text-[10px] text-zinc-400">10 nodes · 47 pods · all green</div>
              </div>
              <div className="border border-zinc-800 rounded">
                <div className="px-2 py-1 text-[10px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800 flex items-center gap-2">
                  <GitBranch className="w-3 h-3" /> GitOps · ArgoCD
                </div>
                <div className="px-2 py-1.5 text-[10px] text-zinc-400">asgard · Synced · Healthy</div>
              </div>
              <div className="border border-zinc-800 rounded">
                <div className="px-2 py-1 text-[10px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800 flex items-center gap-2">
                  <GitPullRequest className="w-3 h-3" /> Gitea PRs
                </div>
                <div className="px-2 py-1.5 text-[10px] text-zinc-400">3 open · 1 awaiting Sovereign merge</div>
              </div>
            </div>
          )}
        </div>

        {/* ========== RIGHT HALF — ACTIVITY SURFACE ========== */}
        <div className="w-1/2 flex flex-col min-h-0">
          {/* Active strip */}
          <div className="border-b border-zinc-800 bg-zinc-900/30 px-2 py-1.5 shrink-0">
            <div className="text-[9px] uppercase tracking-wider text-zinc-600 mb-1">Active · {agents.filter(a => a.status === 'active').length}</div>
            <div className="flex flex-wrap gap-1">
              {agents.filter(a => a.status === 'active').map(a => (
                <button key={a.id} onClick={() => setActiveAgent(a.id)}
                  className={`flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] border transition-colors ${
                    activeAgent === a.id
                      ? 'border-amber-500/50 bg-amber-500/10 text-amber-300'
                      : 'border-zinc-800 text-zinc-400 hover:border-zinc-700'
                  }`}>
                  <StatusDot status={a.status} flag={a.flag} />
                  <span>{a.name}</span>
                </button>
              ))}
            </div>
          </div>

          {/* Context bar — what's being watched */}
          <div className="border-b border-zinc-800 bg-zinc-900/50 px-3 py-1.5 shrink-0 flex items-center gap-2">
            <span className="text-[10px] text-zinc-500">Watching</span>
            <StatusDot status={focused.status} />
            <span className="text-zinc-200 text-xs font-semibold">{focused.name}</span>
            <span className="text-[10px] text-zinc-600">·</span>
            <span className="text-[10px] text-zinc-500 truncate flex-1">{focused.task || 'idle'}</span>
            <button className="flex items-center gap-1 px-2 py-0.5 border border-zinc-800 rounded text-[10px] text-zinc-500 hover:text-zinc-300 hover:border-zinc-700 shrink-0">
              <Hand className="w-2.5 h-2.5" />
              Interject
            </button>
          </div>

          {/* Surface area — Builder | Action | Reviewer | Architect | Idle */}
          {activeAgent === 'ivar' ? (
            // -------- BUILDER SURFACE --------
            <div className="flex-1 flex flex-col min-h-0">
              <div className="flex-1 flex min-h-0">
                {/* repo tree */}
                <div style={{ width: `${leftRailWidth}px` }} className="border-r border-zinc-800 bg-zinc-900/30 overflow-y-auto shrink-0">
                  <TreeNode node={tree} />
                  <div className="mt-2 px-2 py-1.5 border-t border-zinc-800/60 text-[9px] text-zinc-600">
                    <div className="uppercase tracking-wider mb-1">Legend</div>
                    <div className="flex items-center gap-1.5"><span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" /> writing</div>
                    <div className="flex items-center gap-1.5"><span className="w-1.5 h-1.5 rounded-full bg-blue-400/60" /> read</div>
                  </div>
                </div>

                <LeftRailHandle />

                {/* main artifact: file editor */}
                <div className="flex-1 flex flex-col min-w-0">
                  <div className="px-3 py-1 border-b border-zinc-800 bg-zinc-900/30 flex items-center gap-2 shrink-0">
                    <FileCode className="w-3 h-3 text-amber-400" />
                    <span className="text-zinc-300">rbac/gateway-argocd.yaml</span>
                    <span className="text-[10px] text-amber-400 ml-auto flex items-center gap-1">
                      <span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />
                      writing
                    </span>
                  </div>
                  <div className="flex-1 overflow-y-auto bg-zinc-950">
                    {yamlLines.map(line => (
                      <div key={line.n}
                        className={`flex font-mono text-[11px] leading-5 ${
                          line.writing ? 'bg-amber-500/5 border-l-2 border-amber-500' : 'border-l-2 border-transparent'
                        }`}>
                        <span className="w-10 text-right pr-3 text-zinc-700 select-none shrink-0">{line.n}</span>
                        <span className="text-zinc-300">
                          {line.c}
                          {line.writing && (
                            <span className={`inline-block w-2 h-3.5 align-middle ml-px ${
                              cursorBlink ? 'bg-amber-400' : 'bg-transparent'
                            }`} />
                          )}
                        </span>
                      </div>
                    ))}
                  </div>
                </div>
              </div>
              <ActivityTimeline items={ivarActivity} />
            </div>
          ) : activeAgent === 'jeeves' ? (
            // -------- ACTION SURFACE --------
            <div className="flex-1 flex flex-col min-h-0">
              <div className="flex-1 flex min-h-0">

                {/* Context rail: thread + plan */}
                <div style={{ width: `${leftRailWidth}px` }} className="border-r border-zinc-800 bg-zinc-900/30 overflow-y-auto shrink-0">
                  <div className="flex items-center gap-1.5 px-2 py-1 text-xs text-zinc-400 border-b border-zinc-800/60">
                    <Mail className="w-3 h-3 text-blue-400 shrink-0" />
                    <span className="text-zinc-300 truncate">Re: Hirdforge demo</span>
                  </div>

                  {/* Trigger — original email */}
                  <div className="p-3 border-b border-zinc-800/60">
                    <div className="flex items-center gap-2 text-[10px] text-zinc-600 mb-1.5">
                      <span className="text-zinc-400 font-semibold">Sarah Chen</span>
                      <span>·</span>
                      <span>11:32</span>
                    </div>
                    <div className="text-[11px] text-zinc-400 leading-relaxed">
                      Hi Kit, we're huge fans of what you're building. Could we set up a 30-min walkthrough of Hirdforge sometime this week? I'm flexible Wed–Fri afternoons.
                    </div>
                  </div>

                  {/* Plan checklist */}
                  <div className="px-3 py-3">
                    <div className="text-[10px] uppercase tracking-wider text-zinc-500 mb-2">Plan</div>
                    <ul className="space-y-1.5">
                      <li className="flex items-start gap-2 text-[11px]">
                        <CheckCircle2 className="w-3 h-3 text-emerald-500 shrink-0 mt-0.5" />
                        <span className="text-zinc-400">Checked calendar — Fri 2pm available</span>
                      </li>
                      <li className="flex items-start gap-2 text-[11px]">
                        <CheckCircle2 className="w-3 h-3 text-emerald-500 shrink-0 mt-0.5" />
                        <span className="text-zinc-400">Drafted reply</span>
                      </li>
                      <li className="flex items-start gap-2 text-[11px]">
                        <Clock className="w-3 h-3 text-amber-400 shrink-0 mt-0.5" />
                        <span className="text-amber-300">Awaiting Sovereign approval</span>
                      </li>
                      <li className="flex items-start gap-2 text-[11px] opacity-50">
                        <Circle className="w-3 h-3 shrink-0 mt-0.5" />
                        <span>Send reply</span>
                      </li>
                      <li className="flex items-start gap-2 text-[11px] opacity-50">
                        <Circle className="w-3 h-3 shrink-0 mt-0.5" />
                        <span>Send calendar invite</span>
                      </li>
                    </ul>
                  </div>
                </div>

                <LeftRailHandle />

                {/* Artifact: composed email */}
                <div className="flex-1 flex flex-col min-w-0">
                  <div className="px-4 py-1.5 border-b border-zinc-800 bg-zinc-900/30 flex items-center gap-2 shrink-0">
                    <Mail className="w-3.5 h-3.5 text-blue-400" />
                    <span className="text-xs text-zinc-300">Compose email</span>
                    <span className="text-[10px] text-amber-400 ml-auto px-1.5 py-0.5 border border-amber-500/30 rounded flex items-center gap-1">
                      <ShieldAlert className="w-2.5 h-2.5" />
                      destructive_write · awaiting approval
                    </span>
                  </div>

                  <div className="flex-1 overflow-y-auto bg-zinc-950 px-6 py-4 text-xs">
                    <div className="space-y-2 mb-4">
                      <div className="flex gap-3">
                        <span className="text-zinc-500 w-16 shrink-0">To:</span>
                        <span className="text-zinc-300">Sarah Chen &lt;sarah@anthropic.com&gt;</span>
                      </div>
                      <div className="flex gap-3">
                        <span className="text-zinc-500 w-16 shrink-0">Subject:</span>
                        <span className="text-zinc-300">Re: Hirdforge demo</span>
                      </div>
                    </div>
                    <div className="border-t border-zinc-800 pt-4 text-zinc-300 leading-relaxed font-sans text-sm">
                      Hi Sarah,<br /><br />
                      Thanks for reaching out. I'd be glad to walk you through Hirdforge — Kit's multi-agent orchestration platform. Friday at 2pm works on his calendar; I'll send a calendar invite shortly.<br /><br />
                      Best,<br />
                      Jeeves <span className="text-zinc-500 italic text-xs">(on behalf of Kit)</span>
                    </div>
                  </div>

                  {/* Action bar — embedded in the artifact, not a popup */}
                  <div className="px-4 py-3 border-t border-zinc-800 bg-zinc-900/30 flex items-center gap-2 shrink-0">
                    <span className="text-[10px] text-zinc-500 flex items-center gap-1.5">
                      <ShieldAlert className="w-3 h-3" />
                      Lockbox · #queue-7af2
                    </span>
                    <div className="ml-auto flex items-center gap-2">
                      <button className="text-xs px-3 py-1.5 border border-zinc-700 text-zinc-400 rounded hover:bg-zinc-800 hover:text-zinc-200">
                        Reject
                      </button>
                      <button className="flex items-center gap-1.5 text-xs px-3 py-1.5 border border-zinc-700 text-zinc-400 rounded hover:bg-zinc-800 hover:text-zinc-200">
                        <Edit3 className="w-3 h-3" /> Revise
                      </button>
                      <button className="flex items-center gap-1.5 text-xs px-4 py-1.5 bg-amber-500 text-zinc-950 rounded font-semibold hover:bg-amber-400">
                        <Send className="w-3 h-3" />
                        Approve & send
                      </button>
                    </div>
                  </div>
                </div>
              </div>
              <ActivityTimeline items={jeevesActivity} />
            </div>
          ) : (
            <div className="flex-1 flex items-center justify-center text-zinc-600 text-xs">
              <div className="text-center">
                <Pause className="w-6 h-6 mx-auto mb-2 opacity-50" />
                <div>{focused.status === 'idle' ? `${focused.name} is idle.` : `${focused.name}'s surface — design pending`}</div>
                <div className="text-[10px] mt-1 text-zinc-700">
                  Reviewer → PR diff · Architect → dispatch tree · PA → email/calendar
                </div>
              </div>
            </div>
          )}
        </div>
      </div>

      {/* FOOTER */}
      <div className="h-6 border-t border-zinc-800 bg-zinc-900 flex items-center px-4 text-[10px] text-zinc-600 shrink-0">
        <span>asgard cluster · 10 nodes · ArgoCD synced</span>
        <span className="ml-auto">S119 strawman · v0.9 · action surface</span>
      </div>
    </div>
  );
};

const Message = ({ who, name, time, children }) => {
  const isKit = who === 'kit';
  return (
    <div className={`flex flex-col ${isKit ? 'items-end' : 'items-start'}`}>
      <div className="flex items-center gap-2 mb-1 text-[10px] text-zinc-500">
        {!isKit && <span className="font-semibold text-zinc-400">{name}</span>}
        <span>{time}</span>
        {isKit && <span className="font-semibold text-amber-400">{name}</span>}
      </div>
      <div className={`max-w-[85%] px-3 py-2 rounded text-zinc-300 leading-relaxed ${
        isKit ? 'bg-amber-500/10 border border-amber-500/20' : 'bg-zinc-900 border border-zinc-800'
      }`}>
        {children}
      </div>
    </div>
  );
};

export default HirdforgeUI;