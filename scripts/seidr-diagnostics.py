#!/usr/bin/env python3
import argparse, json, sys
from collections import Counter
from datetime import datetime, timedelta
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
D="http://seidr.valhalla.svc:8082"; T=["general","failure","lesson","fact","observation","soul_candidate"]
A=lambda: (lambda p:(p.add_argument("--seidr-url",default=D),p.add_argument("--format",choices=["text","json"],default="text"),p.parse_args()))(argparse.ArgumentParser(description="Generate Seidr health and diagnostics reports"))
def F(b,p):
    try:
        with urlopen(Request(b.rstrip("/")+p,headers={"Accept":"application/json"}),timeout=10) as r: return json.load(r),None
    except HTTPError as e: return None,f"HTTP {e.code} {e.reason}"
    except URLError as e: return None,f"connection error: {e.reason}"
    except json.JSONDecodeError as e: return None,f"invalid JSON: {e}"
def P(v):
    if not isinstance(v,str) or not v.strip(): return None
    try: return datetime.fromisoformat(v.strip().replace("Z","+00:00"))
    except ValueError: return None
I=lambda v:v.isoformat() if v else None

def M(m):
    for k in ("created_at","timestamp","updated_at","last_accessed"):
        t=P(m.get(k))
        if t: return t
C=lambda x:x.get("name") if isinstance(x,dict) else str(x)
G=lambda n:n[:-7] if n.endswith("-memory") else None

def S(h,e):
    return {"ok":e is None and isinstance(h,dict),"error":e,"status":h.get("status") if isinstance(h,dict) else None,"memory_count":h.get("memory_count") if isinstance(h,dict) else None,"collections":(h.get("collections") or []) if isinstance(h,dict) else []}

def K(c,e):
    z=isinstance(c,dict) and bool(c.get("connected") or c.get("brain_connected") or c.get("available") or str(c.get("status","")).lower() in ("connected","online","ok"))
    return {"ok":e is None,"error":e,"connected":z,"details":c if isinstance(c,dict) else None}

def N(mem,now):
    c=Counter(); s=n=0; ts=[]
    for m in mem:
        if not isinstance(m,dict): continue
        c[m.get("type","general")]+=1; i=m.get("importance")
        if isinstance(i,(int,float)): s+=float(i); n+=1
        t=M(m)
        if t: ts.append(t)
    newest=max(ts) if ts else None; oldest=min(ts) if ts else None
    return {"total":len(mem),"types":{k:c.get(k,0) for k in T},"average_importance":round(s/n,3) if n else None,"oldest":I(oldest),"newest":I(newest),"stale":bool((newest and now-newest>timedelta(hours=24)) or (mem and newest is None))}

def R(b):
    now=datetime.now().astimezone(); h,e=F(b,"/health")
    r={"generated_at":now.isoformat(),"service_health":S(h,e),"per_agent":{},"cognitive_processing":{},"distribution":{"average_memories":0,"bloated_agents":[]},"recommendations":[]}
    if e or not isinstance(h,dict): r["recommendations"].append("Seidr health endpoint unavailable — investigate service connectivity"); return r
    agents=sorted(set(a for a in (G(C(i)) for i in r["service_health"]["collections"]) if a)); totals=[]; stale=[]
    for a in agents:
        mem,err=F(b,f"/memories?agent={a}")
        if err or not isinstance(mem,list): r["per_agent"][a]={"error":err or "unexpected response","total":0}; r["recommendations"].append(f"Unable to inspect memories for agent {a}"); continue
        st=N(mem,now); r["per_agent"][a]=st; totals.append(st["total"]); stale += [a] if st["stale"] else []
    c,ce=F(b,"/cognitive/status"); r["cognitive_processing"]=K(c,ce); avg=(sum(totals)/len(totals)) if totals else 0; blo=[a for a,s in r["per_agent"].items() if s.get("total",0)>avg*2 and avg>0]
    r["distribution"]={"average_memories":round(avg,2),"bloated_agents":blo}
    if r["service_health"].get("status") not in ("ok","healthy"): r["recommendations"].append("Seidr health status is not healthy — inspect service logs")
    if ce: r["recommendations"].append("Cognitive processing status unavailable")
    elif not r["cognitive_processing"]["connected"]: r["recommendations"].append("Cognitive processing offline")
    r["recommendations"] += [f"Agent {a} stale — check if active" for a in stale] + [f"Run /consolidate for agent {a}" for a in blo]
    if not r["recommendations"]: r["recommendations"].append("No immediate issues detected")
    return r

def X(r):
    h=r["service_health"]; c=r["cognitive_processing"]; lines=["Seidr Diagnostics Report",f"Generated: {r['generated_at']}","","1. Service Health",f"   Status: {h.get('status') or 'unknown'}",f"   Reachable: {'yes' if h.get('ok') else 'no'}",f"   Memory Count: {h.get('memory_count')}",f"   Collections: {', '.join(C(i) for i in h.get('collections',[])) if h.get('collections') else 'none'}"]
    if h.get("error"): lines += [f"   Error: {h['error']}"]
    lines += ["","2. Per-Agent Memory Stats"]
    if r["per_agent"]:
        for a,s in sorted(r["per_agent"].items()):
            lines += [f"   {a}: error={s['error']}"] if s.get("error") else [f"   {a}: total={s['total']}, avg_importance={s['average_importance']}, " + ", ".join(f"{k}={s['types'][k]}" for k in T)]
    else: lines += ["   No agent memory collections found"]
    lines += ["","3. Cognitive Processing Status",f"   Reachable: {'yes' if c.get('ok') else 'no'}",f"   Connected: {'yes' if c.get('connected') else 'no'}"]
    if c.get("error"): lines += [f"   Error: {c['error']}"]
    elif c.get("details"): lines += [f"   Details: {json.dumps(c['details'],sort_keys=True)}"]
    lines += ["","4. Memory Freshness"]
    lines += [f"   {a}: oldest={s['oldest']}, newest={s['newest']}, stale={'yes' if s['stale'] else 'no'}" for a,s in sorted(r["per_agent"].items()) if not s.get("error")]
    lines += ["","5. Memory Distribution Analysis",f"   Average memories per agent: {r['distribution']['average_memories']}",f"   Potential bloat: {', '.join(r['distribution']['bloated_agents']) if r['distribution']['bloated_agents'] else 'none'}","","6. Recommendations"] + [f"   - {i}" for i in r["recommendations"]]
    return "\n".join(lines)

def main():
    a=A(); r=R(a.seidr_url.rstrip("/")); print(json.dumps(r,indent=2,sort_keys=True) if a.format=="json" else X(r)); return 0
if __name__=="__main__": sys.exit(main())
