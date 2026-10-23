#!/usr/bin/env python3
"""Demo traffic control panel. Stdlib only. Whitelisted dimensions only."""
import json
import os
import time
import threading, os, ssl, time, urllib.request, urllib.parse, urllib.error
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler

NS = "aitra-system"
K8S = "https://kubernetes.default.svc"
SA = "/var/run/secrets/kubernetes.io/serviceaccount"
PROM = os.environ.get("PROM_URL", "http://aitra-meter-prometheus:9090")
VLLM_IMAGE = os.environ.get("VLLM_IMAGE", "vllm/vllm-openai:v0.19.0")
ACCESS_KEY = os.environ.get("ACCESS_KEY", "")
MAX_JOBS = 8
LOADGEN = "vllm-fleet-loadgen"

with open(SA + "/token") as f:
    TOKEN = f.read().strip()
SSLCTX = ssl.create_default_context(cafile=SA + "/ca.crt")

# -------- whitelists --------
MODELS = {
    "qwen3.5-0.8b":            ("vllm-q35-0p8b",     "q35-0p8b"),
    "qwen3.5-2b":              ("vllm-q35-2b",       "q35-2b"),
    "qwen3.5-4b":              ("vllm-q35-4b",       "q35-4b"),
    "qwen3.5-9b":              ("vllm-q35-9b",       "q35-9b"),
    "qwen3.6-27b":             ("vllm-q36-27b",      "q36-27b"),
    "qwen3.6-27b-fp8":         ("vllm-q36-27b-fp8",  "q36-27b-fp8"),
    "qwen3.6-35b-a3b-fp8":     ("vllm-q36-35b-fp8",  "q36-35b-fp8"),
    "qwen3.5-122b-a10b-int4":  ("vllm-q35-122b-int4","q35-122b-int4"),
}

BASE = ("vllm bench serve --backend openai --base-url http://%(svc)s:8000 "
        "--endpoint /v1/completions --model %(model)s --tokenizer /models/%(dir)s ")

SHAPES = {
    "balanced":  {"zh": "均衡对话", "en": "Balanced chat",
                  "args": "--dataset-name random --random-input-len 512 --random-output-len 256 "},
    "summarize": {"zh": "长文摘要", "en": "Long-doc summarize",
                  "args": "--dataset-name random --random-input-len 4096 --random-output-len 64 "},
    "generate":  {"zh": "长文生成", "en": "Long-form generate",
                  "args": "--dataset-name random --random-input-len 64 --random-output-len 1024 "},
    "sharegpt":  {"zh": "真实对话", "en": "Real conversations",
                  "args": "--dataset-name sharegpt --dataset-path /datasets/sharegpt_v3.json "},
}
CONCURRENCY = [1, 4, 8, 16, 32, 64]
DURATIONS = {
    "5m":      {"zh": "5 分钟",  "en": "5 min",  "secs": 300},
    "30m":     {"zh": "30 分钟", "en": "30 min", "secs": 1800},
    "forever": {"zh": "持续(手动停止,上限 4 小时)", "en": "Continuous (stop manually, 4 h cap)", "secs": 14400},
}
# Sweep prompt volume scales with output length so every shape finishes
# inside the deadline (generate emits 4x the tokens of balanced).
# Sized for H100 throughput: bigger per-step batches + shorter gaps keep
# the duty cycle high so fast models don't drain into zero-token display
# windows. Concurrency steps themselves are unchanged.
SWEEP_FACTOR = {"balanced": 1500, "summarize": 2000, "generate": 400, "sharegpt": 1500}
SCENES = {
    "sweep": {"zh": "并发扫描 1→32(约 20 分钟)", "en": "Concurrency sweep 1→32 (~20 min)", "deadline": 3000,
              "script": "for C in 1 2 4 8 16 32; do %(bench)s--max-concurrency $C --num-prompts $((C*%(factor)s)); sleep 15; done"},
    "burst": {"zh": "脉冲流量 ×8(满载/静默各约 30s)", "en": "Burst ×8 (~30 s load / 30 s quiet)", "deadline": 1400,
              "script": "for i in 1 2 3 4 5 6 7 8; do %(bench)s--max-concurrency 32 --num-prompts 6000; sleep 30; done"},
}

# -------- k8s helpers --------
def k8s(method, path, body=None, ctype="application/json"):
    req = urllib.request.Request(K8S + path, method=method)
    req.add_header("Authorization", "Bearer " + TOKEN)
    req.add_header("Content-Type", ctype)
    data = json.dumps(body).encode() if body is not None else None
    with urllib.request.urlopen(req, data=data, context=SSLCTX, timeout=15) as r:
        return json.loads(r.read())

def list_jobs():
    out = []
    res = k8s("GET", f"/apis/batch/v1/namespaces/{NS}/jobs?labelSelector=app=demo-load")
    for j in res.get("items", []):
        st = j.get("status", {})
        state = "running"
        for c in st.get("conditions", []) or []:
            if c.get("type") == "Complete" and c.get("status") == "True":
                state = "done"
            if c.get("type") == "Failed" and c.get("status") == "True":
                state = "failed"
        if state == "running" and not st.get("active"):
            state = "pending"
        ann = j["metadata"].get("annotations") or {}
        out.append({
            "name": j["metadata"]["name"],
            "model": j["metadata"]["labels"].get("demo-model", "?"),
            "desc": ann.get("demo-desc", "?"),
            "state": state,
        })
    return out

def make_job(name, model, desc, script, deadline):
    job = {
        "apiVersion": "batch/v1", "kind": "Job",
        "metadata": {"name": name, "namespace": NS,
                     "labels": {"app": "demo-load", "demo-model": model},
                     "annotations": {"demo-desc": desc}},
        "spec": {
            "activeDeadlineSeconds": deadline,
            "ttlSecondsAfterFinished": 900,
            "backoffLimit": 0,
            "template": {
                "metadata": {"labels": {"app": "demo-load"}},
                "spec": {
                    "restartPolicy": "Never",
                    "nodeSelector": {"kubernetes.io/hostname": "xspark"},
                    "containers": [{
                        "name": "bench",
                        "image": VLLM_IMAGE,
                        "imagePullPolicy": "Never",
                        "command": ["/bin/bash", "-c", script],
                        "env": [{"name": "HF_HUB_OFFLINE", "value": "1"}],
                        "volumeMounts": [{"name": "models", "mountPath": "/models", "readOnly": True},
                                          {"name": "datasets", "mountPath": "/datasets", "readOnly": True}],
                        "resources": {"requests": {"cpu": "500m", "memory": "1Gi"},
                                       "limits": {"cpu": "4", "memory": "8Gi"}},
                    }],
                    "volumes": [{"name": "models",
                                 "hostPath": {"path": "/root/aitra-models", "type": "Directory"}},
                                {"name": "datasets",
                                 "hostPath": {"path": "/root/aitra-datasets", "type": "Directory"}}],
                },
            },
        },
    }
    k8s("POST", f"/apis/batch/v1/namespaces/{NS}/jobs", job)

def guard(model):
    active = [j for j in list_jobs() if j["state"] in ("running", "pending")]
    if len(active) >= MAX_JOBS:
        return f"busy: {len(active)}/{MAX_JOBS} jobs running · 任务已满,请先停止"
    if any(j["model"] == model for j in active):
        return "already running · 该模型已有任务"
    return None

def start_custom(model, shape, conc, duration):
    if (model not in MODELS or shape not in SHAPES
            or conc not in CONCURRENCY or duration not in DURATIONS):
        return None, "invalid parameters · 非法参数"
    err = guard(model)
    if err:
        return None, err
    svc, mdir = MODELS[model]
    bench = (BASE + SHAPES[shape]["args"]) % {"svc": svc, "model": model, "dir": mdir}
    d = DURATIONS[duration]
    # num-prompts sized for H100 throughput (concurrency unchanged): bigger
    # batches mean fewer bench restarts, so the restart gap no longer shows
    # up as zero-token windows on the dashboard.
    if duration == "forever":
        script = (f"while true; do {bench}--max-concurrency {conc} "
                  f"--num-prompts {conc*4000}; done")
        deadline = d["secs"]
    else:
        script = (f"end=$((SECONDS+{d['secs']})); while [ $SECONDS -lt $end ]; do "
                  f"{bench}--max-concurrency {conc} --num-prompts {conc*4000}; done")
        deadline = d["secs"] + 240
    desc = f"{shape} · c{conc} · {duration}"
    name = f"demo-c{conc}-{shape}-{mdir}-{int(time.time())%100000}".lower().replace(".", "-")
    make_job(name, model, desc, script, deadline)
    return name, None

def start_scene(model, shape, scene):
    if model not in MODELS or shape not in SHAPES or scene not in SCENES:
        return None, "invalid parameters · 非法参数"
    err = guard(model)
    if err:
        return None, err
    svc, mdir = MODELS[model]
    bench = (BASE + SHAPES[shape]["args"]) % {"svc": svc, "model": model, "dir": mdir}
    sc = SCENES[scene]
    script = sc["script"] % {"bench": bench, "factor": SWEEP_FACTOR.get(shape, 300)}
    desc = f"{scene} · {shape}"
    name = f"demo-{scene}-{mdir}-{int(time.time())%100000}".lower().replace(".", "-")
    make_job(name, model, desc, script, sc["deadline"])
    return name, None

def stop_all():
    n = 0
    for j in list_jobs():
        try:
            k8s("DELETE", f"/apis/batch/v1/namespaces/{NS}/jobs/{j['name']}?propagationPolicy=Background")
            n += 1
        except urllib.error.HTTPError:
            pass
    return n

def loadgen_replicas():
    try:
        d = k8s("GET", f"/apis/apps/v1/namespaces/{NS}/deployments/{LOADGEN}")
        return d.get("spec", {}).get("replicas", 0)
    except Exception:
        return -1

def loadgen_set(on):
    body = {"spec": {"replicas": 1 if on else 0}}
    k8s("PATCH", f"/apis/apps/v1/namespaces/{NS}/deployments/{LOADGEN}/scale",
        body, ctype="application/merge-patch+json")

def prom_query(q):
    url = PROM + "/api/v1/query?query=" + urllib.parse.quote(q)
    with urllib.request.urlopen(url, timeout=6) as r:
        return json.loads(r.read())["data"]["result"]

def prom_jpt():
    try:
        gpu = {x["metric"].get("model", "?"): round(float(x["value"][1]), 2)
               for x in prom_query('aitra_j_per_token{hardware!="unknown"}')}
    except Exception:
        return {}
    try:
        system = {x["metric"].get("model", "?"): round(float(x["value"][1]), 2)
                  for x in prom_query('aitra_system_j_per_token{hardware!="unknown"}')}
    except Exception:
        system = {}
    # Fleet models only: tenant workloads (platform spaces) are metered but
    # do not belong on the booth console.
    return {m: {"gpu": v, "system": system.get(m)} for m, v in gpu.items()
            if m.startswith("qwen")}

def prom_power():
    try:
        serving = float(prom_query('sum(aitra_gpu_power_watts{gpu_id!="idle"})')[0]["value"][1])
        idle = float(prom_query("sum(aitra_idle_power_watts)")[0]["value"][1])
        return {"serving": round(serving), "idle": round(idle)}
    except Exception:
        return {}

# -------- GPU rack: fleet deployment state + start/stop --------
def model_deploy_state(dep):
    """stopped | loading | serving from one fleet deployment object."""
    spec = dep.get("spec", {}).get("replicas", 0) or 0
    ready = dep.get("status", {}).get("readyReplicas", 0) or 0
    if spec == 0:
        return "stopped"
    return "serving" if ready >= 1 else "loading"

def prom_model_power():
    try:
        return {x["metric"].get("gpu_id", "?"): round(float(x["value"][1]))
                for x in prom_query('aitra_gpu_power_watts{gpu_id!="idle"}')}
    except Exception:
        return {}

def rack_state():
    power = prom_model_power()
    jpt = prom_jpt()
    out = []
    for model, (dep_name, _) in MODELS.items():
        try:
            dep = k8s("GET", f"/apis/apps/v1/namespaces/{NS}/deployments/{dep_name}")
            state = model_deploy_state(dep)
        except Exception as e:
            state = "unknown"
        entry = {"model": model, "state": state}
        if state == "serving":
            entry["power"] = power.get(model)
            j = jpt.get(model)
            entry["jpt"] = j.get("gpu") if isinstance(j, dict) else j
        out.append(entry)
    return out

def model_scale(model, on):
    if model not in MODELS:
        return "unknown model"
    dep_name = MODELS[model][0]
    try:
        k8s("PATCH", f"/apis/apps/v1/namespaces/{NS}/deployments/{dep_name}/scale",
            {"spec": {"replicas": 1 if on else 0}}, ctype="application/merge-patch+json")
        return None
    except urllib.error.HTTPError as e:
        if e.code == 403:
            return "RBAC: console lacks scale permission for fleet deployments"
        return f"k8s {e.code}"
    except Exception as e:
        return str(e)[:120]

# -------- agent task demos (task-gateway metering) --------
TASK_GW = os.environ.get("TASK_GW", "http://aitra-meter-task-gateway.aitra-system:8090")

AGENT_SCENARIOS = {
    "coding": {
        "zh": "编码任务(大模型规划,小模型执行)", "en": "Coding (plan big, execute small)",
        "steps": [
            ("规划/Plan", "qwen3.6-27b", "You are a coding agent. Plan, in 4 short numbered steps, how to add JWT auth to a Go HTTP API.", 160),
            ("执行/Act 1", "qwen3.5-2b", "Execute step 1 of this plan. Reply with only code.\nPlan:\n{plan}", 120),
            ("执行/Act 2", "qwen3.5-2b", "Execute step 2 of this plan. Reply with only code.\nPlan:\n{plan}", 120),
            ("执行/Act 3", "qwen3.5-2b", "Execute step 3 of this plan. Reply with only code.\nPlan:\n{plan}", 120),
            ("评审/Review", "qwen3.5-122b-a10b-int4", "Critically review this plan in 3 bullets:\n{plan}", 140),
            ("总结/Sum", "qwen3.5-9b", "Summarize the session in two sentences.\nPlan:\n{plan}", 90),
        ],
    },
    "research": {
        "zh": "深度研究(MoE 综合)", "en": "Deep research (MoE synthesis)",
        "steps": [
            ("提纲/Outline", "qwen3.6-27b", "Outline, in 3 numbered questions, what to research about datacenter GPU energy efficiency.", 140),
            ("调研/Ask 1", "qwen3.5-9b", "Answer question 1 concisely.\nQuestions:\n{plan}", 130),
            ("调研/Ask 2", "qwen3.5-9b", "Answer question 2 concisely.\nQuestions:\n{plan}", 130),
            ("调研/Ask 3", "qwen3.5-9b", "Answer question 3 concisely.\nQuestions:\n{plan}", 130),
            ("综合/Synth", "qwen3.6-35b-a3b-fp8", "Synthesize the findings into one paragraph.\nNotes:\n{prev}", 150),
        ],
    },
    "cheap": {
        "zh": "全小模型流水线(成本对照)", "en": "All-small pipeline (cost control)",
        "steps": [
            ("规划/Plan", "qwen3.5-2b", "Plan, in 3 numbered steps, how to add JWT auth to a Go HTTP API.", 120),
            ("执行/Act 1", "qwen3.5-2b", "Execute step 1. Reply with only code.\nPlan:\n{plan}", 120),
            ("执行/Act 2", "qwen3.5-2b", "Execute step 2. Reply with only code.\nPlan:\n{plan}", 120),
            ("总结/Sum", "qwen3.5-4b", "Summarize in two sentences.\nPlan:\n{plan}", 90),
        ],
    },
}

AGENT_RUNS = {}          # task_id -> {"scenario","status","started","steps":[...]}
AGENT_LOCK = threading.Lock()

def _agent_chat(task_id, model, prompt, max_tokens):
    req = urllib.request.Request(
        f"{TASK_GW}/t/{task_id}/v1/chat/completions",
        data=json.dumps({"model": model, "max_tokens": max_tokens, "temperature": 0.7,
                         "messages": [{"role": "user", "content": prompt}]}).encode(),
        headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=180) as r:
        body = json.loads(r.read())
    return (body["choices"][0]["message"]["content"],
            (body.get("usage") or {}).get("completion_tokens", 0))

def _agent_run(task_id, key):
    plan = prev = ""
    steps = AGENT_SCENARIOS[key]["steps"]
    for i, (title, model, tmpl, mx) in enumerate(steps):
        with AGENT_LOCK:
            AGENT_RUNS[task_id]["steps"][i]["status"] = "running"
        try:
            text, toks = _agent_chat(task_id, model, tmpl.format(plan=plan, prev=prev), mx)
            if i == 0:
                plan = text
            prev = text
            with AGENT_LOCK:
                AGENT_RUNS[task_id]["steps"][i].update(status="done", tokens=toks)
        except Exception as e:
            with AGENT_LOCK:
                AGENT_RUNS[task_id]["steps"][i].update(status="failed")
                AGENT_RUNS[task_id]["status"] = "failed"
                AGENT_RUNS[task_id]["error"] = str(e)[:120]
            return
        time.sleep(1.2)
    with AGENT_LOCK:
        AGENT_RUNS[task_id]["status"] = "done"

def agent_start(key):
    if key not in AGENT_SCENARIOS:
        return None, "unknown scenario"
    task_id = f"demo-{key}-{int(time.time()) % 100000}"
    with AGENT_LOCK:
        if any(r["status"] == "running" for r in AGENT_RUNS.values()):
            return None, "an agent demo is already running"
        AGENT_RUNS[task_id] = {
            "task_id": task_id, "scenario": key, "status": "running", "started": time.time(),
            "steps": [{"title": t, "model": m, "status": "pending", "tokens": 0}
                      for (t, m, _, _) in AGENT_SCENARIOS[key]["steps"]],
        }
    threading.Thread(target=_agent_run, args=(task_id, key), daemon=True).start()
    return task_id, None

def agent_bill(task_id):
    try:
        with urllib.request.urlopen(f"{TASK_GW}/tasks/{task_id}", timeout=5) as r:
            return json.loads(r.read())
    except Exception:
        return None

def agent_state():
    with AGENT_LOCK:
        runs = sorted(AGENT_RUNS.values(), key=lambda r: -r["started"])[:3]
        runs = [dict(r, steps=[dict(s) for s in r["steps"]]) for r in runs]
    for r in runs:
        r["bill"] = agent_bill(r["task_id"])
    return runs

def fleet_health():
    try:
        series = prom_query('count by (model) (aitra_j_per_token{hardware!="unknown"})')
        models_up = len([x for x in series if x["metric"].get("model", "").startswith("qwen")])
    except Exception:
        models_up = -1
    try:
        host = prom_query("aitra_host_power_watts")
        host_w = round(float(host[0]["value"][1])) if host else 0
    except Exception:
        host_w = 0
    return {"models_up": models_up, "fleet": len(MODELS), "host_w": host_w}

# -------- http --------
with open("/app/index.html", "rb") as f:
    INDEX = f.read()

LOGIN = ("""<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Aitra Demo Control</title>
<style>body{background:#14181A;color:#E8E6E0;font:16px system-ui;display:flex;align-items:center;
justify-content:center;height:100vh;margin:0}form{text-align:center}
input{padding:10px 14px;font-size:15px;border-radius:6px;border:1px solid #2E3639;
background:#1C2225;color:#E8E6E0;width:240px}
button{padding:10px 22px;margin-left:8px;border-radius:6px;border:0;background:#E8A020;
font-weight:700;cursor:pointer}</style></head><body>
<form method="get" action=""><div style="font-size:22px;margin-bottom:6px">⚡ Aitra Demo Control</div>
<div style="color:#9AA4A8;margin-bottom:18px">Access key required · 需要访问密钥</div>
<input name="k" placeholder="access key / 访问密钥" autofocus>
<button>Enter</button></form></body></html>""").encode()

def cookie_key(handler):
    c = handler.headers.get("Cookie", "") or ""
    for part in c.split(";"):
        k, _, v = part.strip().partition("=")
        if k == "dk":
            return v
    return ""

def authed(handler):
    if not ACCESS_KEY:
        return True
    if handler.headers.get("X-Demo-Key", "") == ACCESS_KEY:
        return True
    return cookie_key(handler) == ACCESS_KEY

_STATUS_CACHE = {"at": 0.0, "data": None}
_STATUS_LOCK = threading.Lock()

def status_payload():
    with _STATUS_LOCK:
        if _STATUS_CACHE["data"] is not None and time.time() - _STATUS_CACHE["at"] < 3:
            return _STATUS_CACHE["data"]
    data = {"jobs": list_jobs(), "jpt": prom_jpt(), "power": prom_power(),
            "health": fleet_health(), "rack": rack_state(),
            "loadgen": loadgen_replicas(),
            "models": list(MODELS),
            "shapes": {k: {"zh": v["zh"], "en": v["en"]} for k, v in SHAPES.items()},
            "concurrency": CONCURRENCY,
            "durations": {k: {"zh": v["zh"], "en": v["en"]} for k, v in DURATIONS.items()},
            "scenes": {k: {"zh": v["zh"], "en": v["en"]} for k, v in SCENES.items()}}
    with _STATUS_LOCK:
        _STATUS_CACHE.update(at=time.time(), data=data)
    return data

class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass
    def _json(self, obj, code=200):
        b = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)
    def _page(self, body, code=200, extra_headers=None):
        self.send_response(code)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        for k, v in (extra_headers or []):
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)
    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path in ("", "/") or parsed.path.startswith("/index"):
            qs = urllib.parse.parse_qs(parsed.query)
            key = (qs.get("k") or [""])[0]
            if ACCESS_KEY and key == ACCESS_KEY:
                self._page(INDEX, extra_headers=[
                    ("Set-Cookie", f"dk={ACCESS_KEY}; Path=/; Max-Age=604800; SameSite=Lax")])
                return
            if authed(self):
                self._page(INDEX)
            else:
                self._page(LOGIN, 401)
        elif parsed.path == "/api/agent":
            if not authed(self):
                self._json({"error": "unauthorized"}, 401)
                return
            self._json({"runs": agent_state(),
                        "scenarios": {k: {"zh": v["zh"], "en": v["en"]}
                                      for k, v in AGENT_SCENARIOS.items()}})
        elif parsed.path == "/api/status":
            if not authed(self):
                self._json({"error": "unauthorized"}, 401)
                return
            self._json(status_payload())
        else:
            self._json({"error": "not found"}, 404)
    def do_POST(self):
        if not authed(self):
            self._json({"error": "unauthorized"}, 401)
            return
        ln = int(self.headers.get("Content-Length", 0) or 0)
        try:
            body = json.loads(self.rfile.read(ln) or b"{}")
        except Exception:
            body = {}
        if self.path == "/api/start":
            models = body.get("models")
            if not isinstance(models, list) or not models:
                models = [body.get("model", "")]
            started, errors = [], []
            for m in models[:8]:
                m = str(m)
                if body.get("scene"):
                    name, err = start_scene(m, str(body.get("shape", "balanced")),
                                            str(body.get("scene")))
                else:
                    try:
                        conc = int(body.get("concurrency", 0))
                    except Exception:
                        conc = 0
                    name, err = start_custom(m, str(body.get("shape", "")), conc,
                                             str(body.get("duration", "forever")))
                if err:
                    errors.append(f"{m}: {err}")
                else:
                    started.append(name)
            resp = {"started": started}
            if errors:
                resp["errors"] = errors
            self._json(resp, 200 if started else 400)
        elif self.path == "/api/model/start":
            err = model_scale(str(body.get("model", "")), True)
            self._json({"error": err} if err else {"ok": True}, 400 if err else 200)
        elif self.path == "/api/model/stop":
            err = model_scale(str(body.get("model", "")), False)
            self._json({"error": err} if err else {"ok": True}, 400 if err else 200)
        elif self.path == "/api/agent/start":
            tid, err = agent_start(str(body.get("scenario", "")))
            if err:
                self._json({"error": err}, 400)
            else:
                self._json({"task_id": tid})
        elif self.path == "/api/stop":
            self._json({"stopped": stop_all()})
        elif self.path == "/api/loadgen":
            try:
                loadgen_set(bool(body.get("on")))
                self._json({"loadgen": loadgen_replicas()})
            except Exception as e:
                self._json({"error": str(e)}, 500)
        else:
            self._json({"error": "not found"}, 404)

print("demo-control listening on :8080", flush=True)
ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
