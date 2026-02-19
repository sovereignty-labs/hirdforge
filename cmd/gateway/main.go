package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>Valhalla Command</title>
<style>
:root{color-scheme:dark}*{box-sizing:border-box}
body{margin:0;height:100vh;overflow:hidden;background:#12121f;color:#e0e0e0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
.app{height:100vh;display:grid;grid-template-rows:50px 1fr 80px}
.header{display:flex;justify-content:space-between;align-items:center;padding:0 14px;background:#1a1a2e;border-bottom:1px solid #2d2d44}
.title{font-size:18px;font-weight:700}.badge{background:#0d6efd;color:#fff;padding:4px 10px;border-radius:999px;font-size:12px}
.main{min-height:0;display:grid;grid-template-columns:260px 1fr 280px;gap:10px;padding:10px}
.panel{background:#1a1a2e;border:1px solid #2d2d44;border-radius:12px;min-height:0}
.left{display:flex;flex-direction:column;padding:10px;gap:10px}.left h3,.right h3{margin:2px 0 4px;font-size:12px;letter-spacing:.08em;color:#9fa7d9}
.agent-list{flex:1;overflow:auto;display:flex;flex-direction:column;gap:8px}
.agent-card{background:#22223b;border:1px solid #2d2d44;border-radius:10px;padding:10px;cursor:pointer;display:flex;flex-direction:column;gap:6px}
.agent-card.sel{border-color:#0d6efd;box-shadow:0 0 0 1px #0d6efd inset,0 0 16px rgba(13,110,253,.2)}
.agent-top{display:flex;justify-content:space-between;align-items:center}.agent-name{font-weight:700}
.dot{width:10px;height:10px;border-radius:50%;display:inline-block}.ok{background:#2bd576;box-shadow:0 0 8px #2bd576}.bad{background:#ff5f6d;box-shadow:0 0 8px #ff5f6d}.warn{background:#ffc107;box-shadow:0 0 8px #ffc107}
.muted{font-size:12px;color:#b8bfde}.tiny{font-size:11px;color:#a3acd9}
.pills{display:flex;flex-wrap:wrap;gap:4px}.pill{font-size:11px;padding:2px 6px;border-radius:999px;background:#101a35;border:1px solid #1d3d7a;color:#cde0ff}
.refresh{border:0;background:#0d6efd;color:#fff;border-radius:10px;padding:9px 10px;font-weight:600;cursor:pointer}
.center{display:grid;grid-template-rows:38px 1fr 56px;min-height:0}
.chat-head{display:flex;align-items:center;gap:8px;padding:0 12px;border-bottom:1px solid #2d2d44}
.messages{overflow:auto;padding:12px;display:flex;flex-direction:column;gap:10px}
.msg{max-width:85%;padding:10px 12px;border-radius:12px;white-space:pre-wrap;line-height:1.35}
.msg.user{align-self:flex-end;background:#0d6efd;color:#fff;border-bottom-right-radius:6px}
.msg.assistant{align-self:flex-start;background:#22223b;border:1px solid #2d2d44;border-bottom-left-radius:6px}
.tools{display:flex;flex-direction:column;gap:8px;margin-top:8px}
details.toolbox{background:#151529;border:1px solid #2d2d44;border-radius:10px;padding:6px 10px;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
details.toolbox summary{cursor:pointer;color:#cdd7ff}details.toolbox pre{margin:8px 0 0;background:#0d0f1f;border:1px solid #242744;border-radius:8px;padding:10px;overflow:auto;color:#d3d8ef}
.inputbar{display:flex;gap:10px;padding:10px;border-top:1px solid #2d2d44}#input{flex:1;border:1px solid #474a70;border-radius:10px;background:#141526;color:#e0e0e0;padding:11px 12px}
#send{border:0;background:#0d6efd;color:#fff;border-radius:10px;padding:0 16px;font-weight:700;cursor:pointer}
#send:disabled,.refresh:disabled{opacity:.55;cursor:not-allowed}
.right{display:grid;grid-template-rows:1fr 1fr;gap:10px;padding:10px}
.cluster-box{background:#151528;border:1px solid #2d2d44;border-radius:10px;padding:8px;display:flex;flex-direction:column;min-height:0}
.rows{overflow:auto;display:flex;flex-direction:column;gap:6px}.node,.pod{display:grid;align-items:center;gap:6px;background:#20223a;border:1px solid #2d2d44;border-radius:8px;padding:6px 8px;font-size:12px}
.node{grid-template-columns:1fr auto}.pod{grid-template-columns:1fr auto auto}.role{font-size:10px;border-radius:999px;padding:2px 6px;background:#2b2f4f;color:#d8ddff}.cp{background:#12385f}.wk{background:#3a2b1a}
.eventbar{border-top:1px solid #2d2d44;background:#1a1a2e;padding:8px 10px;overflow-x:auto;overflow-y:hidden;white-space:nowrap}
.events{display:flex;gap:8px;min-width:max-content}.ev{display:inline-flex;align-items:center;gap:6px;padding:8px 10px;border-radius:10px;border:1px solid #2d2d44;background:#22223b;font-size:12px}
.ev.message{border-color:#1d3d7a;background:#122140}.ev.tool_call{border-color:#6d39b6;background:#2a1b45}.ev.health_change{border-color:#1d7a53;background:#153428}.ev.agent_start{border-color:#6c757d;background:#2b2f36}.ev.k8s_event{border-color:#856404;background:#3b3212}
.disabled{pointer-events:none;opacity:.6}
@media (max-width:1100px){.main{grid-template-columns:260px 1fr}.right{display:none}}
</style>
</head>
<body>
<div class="app">
  <div class="header"><div class="title">⚔️ Valhalla Command</div><div id="badge" class="badge">0/0 healthy</div></div>
  <div class="main">
    <aside id="left" class="panel left">
      <h3>AGENTS</h3>
      <div id="agentList" class="agent-list"></div>
      <button id="refreshAgents" class="refresh">Refresh Agents</button>
    </aside>
    <section class="panel center">
      <div id="chatHead" class="chat-head"><span class="dot bad"></span><span>Talking to: none</span></div>
      <div id="messages" class="messages"></div>
      <div class="inputbar"><input id="input" type="text" placeholder="Send to selected agent..."/><button id="send">Send</button></div>
    </section>
    <aside class="panel right">
      <div class="cluster-box"><h3>NODES</h3><div id="nodes" class="rows"></div></div>
      <div class="cluster-box"><h3>PODS</h3><div id="pods" class="rows"></div></div>
    </aside>
  </div>
  <div class="eventbar"><div id="events" class="events"></div></div>
</div>
<script>
const agentListEl=document.getElementById("agentList"),messagesEl=document.getElementById("messages"),inputEl=document.getElementById("input"),sendEl=document.getElementById("send"),badgeEl=document.getElementById("badge"),chatHeadEl=document.getElementById("chatHead"),leftEl=document.getElementById("left"),eventsEl=document.getElementById("events"),nodesEl=document.getElementById("nodes"),podsEl=document.getElementById("pods");
let agents=[],selectedAgent="",streaming=false,currentAssistant=null;
const sessionsByAgent={},agentMessages={},pendingByAgent={};
function rndHex(){return Math.floor(Math.random()*Number.MAX_SAFE_INTEGER).toString(16)}
function fmtUptime(sec){sec=Number(sec)||0;const h=Math.floor(sec/3600),m=Math.floor((sec%3600)/60),s=sec%60;if(h>0)return h+"h "+m+"m";if(m>0)return m+"m";return s+"s"}
function esc(s){return String(s==null?"":s)}
function scrollBottom(){messagesEl.scrollTop=messagesEl.scrollHeight}
function setStreaming(v){streaming=v;sendEl.disabled=v;inputEl.disabled=v;leftEl.classList.toggle("disabled",v)}
function clearChildren(el){while(el.firstChild)el.removeChild(el.firstChild)}
function saveAgentView(name){if(!name)return;agentMessages[name]=Array.from(messagesEl.children).map(n=>n.cloneNode(true))}
function restoreAgentView(name){clearChildren(messagesEl);const arr=agentMessages[name]||[];for(const n of arr)messagesEl.appendChild(n.cloneNode(true));scrollBottom()}
function ensureSession(agent){if(!sessionsByAgent[agent])sessionsByAgent[agent]=rndHex();return sessionsByAgent[agent]}
function pendingMap(agent){if(!pendingByAgent[agent])pendingByAgent[agent]={};return pendingByAgent[agent]}
function bubble(text,role){const el=document.createElement("div");el.className="msg "+role;el.textContent=text||"";messagesEl.appendChild(el);scrollBottom();return el}
function toolHeader(tool,args){let preview="";if(args&&typeof args==="object"&&"command" in args)preview=String(args.command);else if(args!==undefined)preview=JSON.stringify(args);return "🔨 "+tool+(preview?": "+preview:"")}
function addToolCall(agent,tool,args){if(!currentAssistant)currentAssistant=bubble("","assistant");let wrap=currentAssistant.querySelector(".tools");if(!wrap){wrap=document.createElement("div");wrap.className="tools";currentAssistant.appendChild(wrap)}const d=document.createElement("details");d.className="toolbox";d.open=true;const s=document.createElement("summary");s.textContent=toolHeader(tool,args);const p=document.createElement("pre");p.textContent="running...";d.append(s,p);wrap.appendChild(d);const pm=pendingMap(agent);if(!pm[tool])pm[tool]=[];pm[tool].push(p);scrollBottom()}
function setToolResult(agent,tool,result){const pm=pendingMap(agent);const q=pm[tool]||[];const pre=q.shift();if(!pre)return;const out=result&&result.output?String(result.output):"";const err=result&&result.error?String(result.error):"";pre.textContent=err?(out?err+"\n"+out:err):out;scrollBottom()}
function parseSSE(block){let data="";for(const l of block.split("\n")){if(l.startsWith("data:"))data+=l.slice(5).trimStart()}if(!data)return null;try{return JSON.parse(data)}catch{return null}}

function selectedMeta(){return agents.find(a=>a.name===selectedAgent)||null}
function renderAgents(){
  let healthy=0;clearChildren(agentListEl);
  for(const a of agents){if(a.healthy)healthy++;const card=document.createElement("div");card.className="agent-card"+(a.name===selectedAgent?" sel":"");card.onclick=()=>selectAgent(a.name);
    const top=document.createElement("div");top.className="agent-top";
    const nm=document.createElement("div");nm.className="agent-name";nm.textContent=a.name;
    const dot=document.createElement("span");dot.className="dot "+(a.healthy?"ok":"bad");top.append(nm,dot);
    const model=document.createElement("div");model.className="muted";model.textContent=a.model||"model: unknown";
    const pills=document.createElement("div");pills.className="pills";(a.tools||[]).forEach(t=>{const p=document.createElement("span");p.className="pill";p.textContent=t;pills.appendChild(p)});
    const up=document.createElement("div");up.className="tiny";up.textContent="uptime: "+fmtUptime(a.uptime_seconds||0);
    const stats=document.createElement("div");stats.className="tiny";stats.textContent=(a.requests_served||0)+" requests • "+(a.tool_calls_made||0)+" tool calls";
    card.append(top,model,pills,up,stats);agentListEl.appendChild(card);
  }
  badgeEl.textContent=healthy+"/"+agents.length+" healthy";
}
function updateChatHead(){const m=selectedMeta();if(!m){chatHeadEl.innerHTML='<span class="dot bad"></span><span>Talking to: none</span>';return}chatHeadEl.innerHTML='<span class="dot '+(m.healthy?'ok':'bad')+'"></span><span>Talking to: '+esc(m.name)+'</span>'}
function selectAgent(name){if(streaming)return;saveAgentView(selectedAgent);selectedAgent=name;ensureSession(name);currentAssistant=null;restoreAgentView(name);renderAgents();updateChatHead();inputEl.focus()}

async function loadAgents(){
  try{const r=await fetch('/api/v1/agents');if(!r.ok)throw new Error('agents request failed');agents=await r.json();
    if(!selectedAgent&&agents.length)selectedAgent=agents[0].name;
    if(selectedAgent&&!agents.find(a=>a.name===selectedAgent)){saveAgentView(selectedAgent);selectedAgent=agents.length?agents[0].name:""}
    renderAgents();updateChatHead();if(selectedAgent)restoreAgentView(selectedAgent);
  }catch(e){console.error(e)}
}
async function loadNodes(){
  try{const r=await fetch('/api/v1/cluster/nodes');if(!r.ok)throw new Error('nodes failed');const nodes=await r.json();clearChildren(nodesEl);nodes.forEach(n=>{const row=document.createElement('div');row.className='node';const left=document.createElement('div');left.innerHTML='<div>'+esc(n.name)+'</div><div class="tiny">'+esc(n.kubelet_version||'')+'</div>';const right=document.createElement('div');const dot=document.createElement('span');dot.className='dot '+((n.status||'')==='Ready'?'ok':'bad');const role=document.createElement('span');const rs=n.roles||[];role.className='role '+((rs.join(',').toLowerCase().includes('control-plane')||rs.join(',').toLowerCase().includes('master'))?'cp':'wk');role.textContent=(role.className.includes('cp')?'CP':'Worker');right.append(dot,document.createTextNode(' '),role);row.append(left,right);nodesEl.appendChild(row)})
  }catch(e){console.error(e)}
}
function podDot(status){status=String(status||'');if(status==='Running')return 'ok';if(status==='Pending')return 'warn';return 'bad'}
async function loadPods(){
  try{const r=await fetch('/api/v1/cluster/pods');if(!r.ok)throw new Error('pods failed');const pods=await r.json();clearChildren(podsEl);pods.forEach(p=>{const row=document.createElement('div');row.className='pod';const n=document.createElement('div');n.innerHTML='<div>'+esc(p.name)+'</div><div class="tiny">'+esc(p.node)+'</div>';const rs=document.createElement('div');rs.className='tiny';rs.textContent='r'+(p.restarts||0);const st=document.createElement('div');const dot=document.createElement('span');dot.className='dot '+podDot(p.status);st.append(dot,document.createTextNode(' '+esc(p.status||'')));row.append(n,rs,st);podsEl.appendChild(row)})
  }catch(e){console.error(e)}
}
async function loadEvents(){
  try{const r=await fetch('/api/v1/events');if(!r.ok)throw new Error('events failed');const ev=await r.json();clearChildren(eventsEl);ev.forEach(e=>{const c=document.createElement('div');c.className='ev '+(e.type||'');const tm=(e.time||'').slice(11,16);c.textContent=tm+' • '+(e.agent||'-')+' • '+(e.type||'')+': '+(e.summary||'');eventsEl.appendChild(c)})
  }catch(e){console.error(e)}
}

async function sendMessage(){
  const text=inputEl.value.trim();if(!text||streaming||!selectedAgent)return;
  const meta=selectedMeta();if(!meta||!meta.healthy){bubble('Selected agent is unavailable','assistant');saveAgentView(selectedAgent);return}
  setStreaming(true);bubble(text,'user');currentAssistant=bubble('', 'assistant');inputEl.value='';
  try{
    const resp=await fetch('/api/v1/message',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({agent:selectedAgent,content:text,session_id:ensureSession(selectedAgent)})});
    if(!resp.ok||!resp.body)throw new Error('request failed');
    const reader=resp.body.getReader(),dec=new TextDecoder();let buf='';
    while(true){const part=await reader.read();if(part.done)break;buf+=dec.decode(part.value,{stream:true});for(;;){const i=buf.indexOf('\n\n');if(i<0)break;const evt=parseSSE(buf.slice(0,i));buf=buf.slice(i+2);if(!evt)continue;
      if(evt.type==='content'&&evt.content!==undefined){currentAssistant.textContent+=evt.content}
      else if(evt.type==='tool_call'){addToolCall(selectedAgent,evt.tool||'tool',evt.args)}
      else if(evt.type==='tool_result'){setToolResult(selectedAgent,evt.tool||'tool',evt.result||{})}
      else if(evt.type==='done'){setStreaming(false)}
      scrollBottom();saveAgentView(selectedAgent);
    }}
  }catch(err){bubble('Error: '+(err&&err.message?err.message:String(err)),'assistant')}
  finally{setStreaming(false);saveAgentView(selectedAgent);inputEl.focus()}
}

document.getElementById('refreshAgents').onclick=loadAgents;
sendEl.onclick=sendMessage;
inputEl.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();sendMessage()}});
loadAgents();loadNodes();loadPods();loadEvents();
setInterval(loadAgents,30000);setInterval(()=>{loadNodes();loadPods()},30000);setInterval(loadEvents,10000);
inputEl.focus();
</script>
</body>
</html>`

type Agent struct {
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Healthy        bool     `json:"healthy"`
	Model          string   `json:"model"`
	Tools          []string `json:"tools"`
	UptimeSeconds  int      `json:"uptime_seconds"`
	RequestsServed int64    `json:"requests_served"`
	ToolCallsMade  int64    `json:"tool_calls_made"`
}

type Event struct {
	Time    string `json:"time"`
	Type    string `json:"type"`
	Agent   string `json:"agent"`
	Summary string `json:"summary"`
}

type PodInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	Node      string `json:"node"`
	Restarts  int64  `json:"restarts"`
	Age       string `json:"age"`
	Ready     bool   `json:"ready"`
	Image     string `json:"image"`
}

type NodeInfo struct {
	Name              string   `json:"name"`
	Status            string   `json:"status"`
	Roles             []string `json:"roles"`
	KubeletVersion    string   `json:"kubelet_version"`
	OS                string   `json:"os"`
	Architecture      string   `json:"architecture"`
	AllocatableCPU    string   `json:"allocatable_cpu"`
	AllocatableMemory string   `json:"allocatable_memory"`
}

type messageReq struct {
	Agent     string `json:"agent"`
	Content   string `json:"content"`
	SessionID string `json:"session_id"`
}

type agentHealthResponse struct {
	Status         string   `json:"status"`
	Model          string   `json:"model"`
	Tools          []string `json:"tools"`
	UptimeSeconds  int      `json:"uptime_seconds"`
	RequestsServed int64    `json:"requests_served"`
	ToolCallsMade  int64    `json:"tool_calls_made"`
}

type gateway struct {
	mu       sync.RWMutex
	agents   map[string]*Agent
	order    []string
	eventMu  sync.Mutex
	events   []Event
	eventCap int
	k8s      *k8sState
}

type k8sState struct {
	enabled bool
	client  *http.Client
	token   string
	podNS   string
	mu      sync.RWMutex
	pods    []PodInfo
	nodes   []NodeInfo
}

func die(msg string, err error) { log.Fatalf("%s: %v", msg, err) }

func parseAgents(raw string) (map[string]*Agent, []string, error) {
	out := map[string]*Agent{}
	order := []string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, url, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(url) == "" {
			return nil, nil, fmt.Errorf("invalid agent entry %q", part)
		}
		name, url = strings.TrimSpace(name), strings.TrimRight(strings.TrimSpace(url), "/")
		if _, exists := out[name]; exists {
			return nil, nil, fmt.Errorf("duplicate agent name %q", name)
		}
		out[name] = &Agent{Name: name, URL: url}
		order = append(order, name)
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("no agents configured")
	}
	sort.Strings(order)
	return out, order, nil
}

func (g *gateway) addEvent(eventType, agent, summary string) {
	e := Event{Time: time.Now().Format(time.RFC3339), Type: eventType, Agent: agent, Summary: summary}
	g.eventMu.Lock()
	if len(g.events) == g.eventCap {
		copy(g.events, g.events[1:])
		g.events[len(g.events)-1] = e
	} else {
		g.events = append(g.events, e)
	}
	g.eventMu.Unlock()
}

func (g *gateway) eventsNewest() []Event {
	g.eventMu.Lock()
	defer g.eventMu.Unlock()
	out := make([]Event, len(g.events))
	for i := range g.events {
		out[i] = g.events[len(g.events)-1-i]
	}
	return out
}

func (g *gateway) snapshotAgents() []Agent {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Agent, 0, len(g.order))
	for _, n := range g.order {
		if a := g.agents[n]; a != nil {
			out = append(out, *a)
		}
	}
	return out
}

func (g *gateway) getAgent(name string) (*Agent, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	a, ok := g.agents[name]
	if !ok {
		return nil, false
	}
	cp := *a
	return &cp, true
}

func (g *gateway) updateAgent(updated Agent) {
	g.mu.Lock()
	old := g.agents[updated.Name]
	if old == nil {
		g.mu.Unlock()
		return
	}
	changed := old.Healthy != updated.Healthy
	*old = updated
	g.mu.Unlock()
	if changed {
		state := "unhealthy"
		if updated.Healthy {
			state = "healthy"
		}
		msg := fmt.Sprintf("%s became %s", updated.Name, state)
		log.Printf("agent health changed: %s", msg)
		g.addEvent("health_change", updated.Name, msg)
	}
}

func (g *gateway) refreshAgentHealth() {
	client := &http.Client{Timeout: 2 * time.Second}
	g.mu.RLock()
	names := append([]string(nil), g.order...)
	urls := make(map[string]string, len(g.order))
	for _, n := range g.order {
		urls[n] = g.agents[n].URL
	}
	g.mu.RUnlock()

	for _, name := range names {
		updated := Agent{Name: name, URL: urls[name], Tools: []string{}}
		h, err := queryAgentHealth(client, urls[name])
		if err == nil {
			updated.Healthy = true
			updated.Model = h.Model
			updated.Tools = h.Tools
			updated.UptimeSeconds = h.UptimeSeconds
			updated.RequestsServed = h.RequestsServed
			updated.ToolCallsMade = h.ToolCallsMade
		} else {
			updated.Healthy = false
			updated.Model = ""
			updated.Tools = []string{}
			updated.UptimeSeconds = 0
			updated.RequestsServed = 0
			updated.ToolCallsMade = 0
		}
		g.updateAgent(updated)
	}
}

func queryAgentHealth(client *http.Client, baseURL string) (agentHealthResponse, error) {
	var h agentHealthResponse
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/health")
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return h, fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return h, err
	}
	return h, nil
}

func initK8s() *k8sState {
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	nsPath := "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	tok, tokErr := os.ReadFile(tokenPath)
	ca, caErr := os.ReadFile(caPath)
	ns, nsErr := os.ReadFile(nsPath)
	if tokErr != nil || caErr != nil || nsErr != nil {
		log.Printf("k8s integration disabled (serviceaccount files not found)")
		return &k8sState{enabled: false, podNS: "valhalla"}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		log.Printf("k8s integration disabled (invalid CA cert)")
		return &k8sState{enabled: false, podNS: "valhalla"}
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	client := &http.Client{Timeout: 5 * time.Second, Transport: tr}
	namespace := strings.TrimSpace(string(ns))
	if namespace == "" {
		namespace = "valhalla"
	}
	return &k8sState{enabled: true, client: client, token: strings.TrimSpace(string(tok)), podNS: namespace}
}

func (k *k8sState) setPods(p []PodInfo) {
	k.mu.Lock()
	k.pods = p
	k.mu.Unlock()
}

func (k *k8sState) setNodes(n []NodeInfo) {
	k.mu.Lock()
	k.nodes = n
	k.mu.Unlock()
}

func (k *k8sState) snapshotPods() []PodInfo {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]PodInfo, len(k.pods))
	copy(out, k.pods)
	return out
}

func (k *k8sState) snapshotNodes() []NodeInfo {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]NodeInfo, len(k.nodes))
	copy(out, k.nodes)
	return out
}

func (k *k8sState) get(path string) (map[string]interface{}, error) {
	req, err := http.NewRequest(http.MethodGet, "https://kubernetes.default.svc"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}
func asSlice(v interface{}) []interface{} {
	s, _ := v.([]interface{})
	return s
}
func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}
func asInt64(v interface{}) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	default:
		return 0
	}
}

func ageFrom(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func parsePods(body map[string]interface{}) []PodInfo {
	items := asSlice(body["items"])
	pods := make([]PodInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		meta := asMap(im["metadata"])
		spec := asMap(im["spec"])
		status := asMap(im["status"])
		ready := false
		for _, c := range asSlice(status["conditions"]) {
			cm := asMap(c)
			if asString(cm["type"]) == "Ready" && asString(cm["status"]) == "True" {
				ready = true
				break
			}
		}
		restarts := int64(0)
		image := ""
		cs := asSlice(status["containerStatuses"])
		if len(cs) > 0 {
			first := asMap(cs[0])
			restarts = asInt64(first["restartCount"])
			image = asString(first["image"])
		}
		if image == "" {
			containers := asSlice(spec["containers"])
			if len(containers) > 0 {
				image = asString(asMap(containers[0])["image"])
			}
		}
		pods = append(pods, PodInfo{
			Name:      asString(meta["name"]),
			Namespace: asString(meta["namespace"]),
			Status:    asString(status["phase"]),
			Node:      asString(spec["nodeName"]),
			Restarts:  restarts,
			Age:       ageFrom(asString(meta["creationTimestamp"])),
			Ready:     ready,
			Image:     image,
		})
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods
}

func parseNodes(body map[string]interface{}) []NodeInfo {
	items := asSlice(body["items"])
	nodes := make([]NodeInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		meta := asMap(im["metadata"])
		status := asMap(im["status"])
		labels := asMap(meta["labels"])
		roles := []string{}
		for k := range labels {
			if strings.HasPrefix(k, "node-role.kubernetes.io/") {
				r := strings.TrimPrefix(k, "node-role.kubernetes.io/")
				if r == "" {
					r = "control-plane"
				}
				roles = append(roles, r)
			}
		}
		if len(roles) == 0 {
			roles = []string{"worker"}
		}
		sort.Strings(roles)
		nodeStatus := "Unknown"
		for _, c := range asSlice(status["conditions"]) {
			cm := asMap(c)
			if asString(cm["type"]) == "Ready" {
				if asString(cm["status"]) == "True" {
					nodeStatus = "Ready"
				} else {
					nodeStatus = "NotReady"
				}
				break
			}
		}
		nodeInfo := asMap(status["nodeInfo"])
		alloc := asMap(status["allocatable"])
		nodes = append(nodes, NodeInfo{
			Name:              asString(meta["name"]),
			Status:            nodeStatus,
			Roles:             roles,
			KubeletVersion:    asString(nodeInfo["kubeletVersion"]),
			OS:                asString(nodeInfo["osImage"]),
			Architecture:      asString(nodeInfo["architecture"]),
			AllocatableCPU:    asString(alloc["cpu"]),
			AllocatableMemory: asString(alloc["memory"]),
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes
}

func (k *k8sState) refreshPods() error {
	path := "/api/v1/namespaces/valhalla/pods"
	body, err := k.get(path)
	if err != nil {
		k.setPods(nil)
		return err
	}
	k.setPods(parsePods(body))
	return nil
}

func (k *k8sState) refreshNodes() error {
	body, err := k.get("/api/v1/nodes")
	if err != nil {
		k.setNodes(nil)
		return err
	}
	k.setNodes(parseNodes(body))
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func main() {
	port := flag.String("port", "8080", "HTTP port")
	agentsFlag := flag.String("agents", "", "comma-separated name=url agent list")
	flag.Parse()
	if strings.TrimSpace(*agentsFlag) == "" {
		die("missing --agents", fmt.Errorf("required"))
	}

	agents, order, err := parseAgents(*agentsFlag)
	if err != nil {
		die("failed to parse --agents", err)
	}
	gw := &gateway{agents: agents, order: order, events: make([]Event, 0, 200), eventCap: 200, k8s: initK8s()}
	gw.addEvent("agent_start", "gateway", fmt.Sprintf("Gateway started with %d agents", len(order)))
	gw.refreshAgentHealth()

	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			gw.refreshAgentHealth()
		}
	}()

	if gw.k8s.enabled {
		gw.addEvent("k8s_event", "cluster", "Kubernetes integration enabled")
		if err := gw.k8s.refreshPods(); err != nil {
			gw.addEvent("k8s_event", "cluster", "pod refresh error: "+err.Error())
		}
		if err := gw.k8s.refreshNodes(); err != nil {
			gw.addEvent("k8s_event", "cluster", "node refresh error: "+err.Error())
		}
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				if err := gw.k8s.refreshPods(); err != nil {
					gw.addEvent("k8s_event", "cluster", "pod refresh error: "+err.Error())
				}
			}
		}()
		go func() {
			t := time.NewTicker(60 * time.Second)
			defer t.Stop()
			for range t.C {
				if err := gw.k8s.refreshNodes(); err != nil {
					gw.addEvent("k8s_event", "cluster", "node refresh error: "+err.Error())
				}
			}
		}()
	} else {
		gw.addEvent("k8s_event", "cluster", "Kubernetes integration disabled")
	}

	proxyClient := &http.Client{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, dashboardHTML)
	})
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, gw.snapshotAgents())
	})
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, gw.eventsNewest())
	})
	mux.HandleFunc("/api/v1/cluster/pods", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []PodInfo{})
			return
		}
		writeJSON(w, http.StatusOK, gw.k8s.snapshotPods())
	})
	mux.HandleFunc("/api/v1/cluster/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []NodeInfo{})
			return
		}
		writeJSON(w, http.StatusOK, gw.k8s.snapshotNodes())
	})
	mux.HandleFunc("/api/v1/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in messageReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		agent, ok := gw.getAgent(in.Agent)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown agent"})
			return
		}
		if !agent.Healthy {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent is unhealthy"})
			return
		}
		gw.addEvent("message", in.Agent, fmt.Sprintf("Message sent to %s", in.Agent))

		body, _ := json.Marshal(map[string]string{"content": in.Content, "session_id": in.SessionID})
		uReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(body))
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to create upstream request"})
			return
		}
		uReq.Header.Set("Content-Type", "application/json")
		uResp, err := proxyClient.Do(uReq)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream request failed"})
			return
		}
		defer uResp.Body.Close()
		if uResp.StatusCode < 200 || uResp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(uResp.Body, 4096))
			writeJSON(w, uResp.StatusCode, map[string]string{"error": strings.TrimSpace(string(b))})
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		reader := bufio.NewReader(uResp.Body)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				if _, wErr := w.Write(line); wErr != nil {
					return
				}
				if strings.HasPrefix(string(bytes.TrimSpace(line)), "data:") && bytes.Contains(line, []byte(`"type":"tool_call"`)) {
					gw.addEvent("tool_call", in.Agent, "Tool call observed")
				}
				flusher.Flush()
			}
			if err == io.EOF {
				return
			}
			if err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agents := gw.snapshotAgents()
		healthy := 0
		for _, a := range agents {
			if a.Healthy {
				healthy++
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ready", "agents": len(agents), "healthy": healthy})
	})

	addr := ":" + *port
	log.Printf("Valhalla Gateway listening on %s", addr)
	log.Printf("Agents: %s", strings.Join(order, ","))
	die("gateway failed", http.ListenAndServe(addr, mux))
}
