"""Agent-task energy demo panel (in-cluster). Launches scripted agent runs
through the task-gateway and serves a live bilingual page: steps + energy bill.
Stdlib only, mirrors the demo-control deployment pattern."""
import json
import os
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

GATEWAY = os.environ.get("TASK_GW", "http://aitra-meter-task-gateway.aitra-system:8090")
ROOT = Path(__file__).parent


def _chat(task_id, model, prompt, max_tokens):
    req = urllib.request.Request(
        f"{GATEWAY}/t/{task_id}/v1/chat/completions",
        data=json.dumps({
            "model": model,
            "messages": [{"role": "user", "content": prompt}],
            "max_tokens": max_tokens,
            "temperature": 0.7,
        }).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=180) as r:
        body = json.load(r)
    text = body["choices"][0]["message"]["content"]
    usage = body.get("usage", {})
    return text, usage.get("completion_tokens", 0)


# Step titles are {zh,en}; the frontend picks by language.
SCENARIOS = {
    "coding": {
        "name": {"zh": "编码任务(大模型规划,小模型执行)", "en": "Coding task (plan big, execute small)"},
        "steps": [
            ({"zh": "规划", "en": "Plan"}, "qwen3.6-27b", "You are a coding agent. Plan, in 4 short numbered steps, how to add JWT auth to a Go HTTP API.", 160),
            ({"zh": "执行 1", "en": "Act 1"}, "qwen3.5-2b", "Execute step 1 of this plan. Reply with only code.\nPlan:\n{plan}", 120),
            ({"zh": "执行 2", "en": "Act 2"}, "qwen3.5-2b", "Execute step 2 of this plan. Reply with only code.\nPlan:\n{plan}", 120),
            ({"zh": "执行 3", "en": "Act 3"}, "qwen3.5-2b", "Execute step 3 of this plan. Reply with only code.\nPlan:\n{plan}", 120),
            ({"zh": "评审", "en": "Review"}, "qwen3.5-122b-a10b-int4", "Critically review this plan in 3 bullets:\n{plan}", 140),
            ({"zh": "总结", "en": "Summarize"}, "qwen3.5-9b", "Summarize the session in two sentences.\nPlan:\n{plan}", 90),
        ],
    },
    "research": {
        "name": {"zh": "深度研究(MoE 综合)", "en": "Deep research (MoE synthesis)"},
        "steps": [
            ({"zh": "提纲", "en": "Outline"}, "qwen3.6-27b", "Outline, in 3 numbered questions, what to research about datacenter GPU energy efficiency.", 140),
            ({"zh": "调研 1", "en": "Ask 1"}, "qwen3.5-9b", "Answer question 1 concisely.\nQuestions:\n{plan}", 130),
            ({"zh": "调研 2", "en": "Ask 2"}, "qwen3.5-9b", "Answer question 2 concisely.\nQuestions:\n{plan}", 130),
            ({"zh": "调研 3", "en": "Ask 3"}, "qwen3.5-9b", "Answer question 3 concisely.\nQuestions:\n{plan}", 130),
            ({"zh": "综合", "en": "Synthesize"}, "qwen3.6-35b-a3b-fp8", "Synthesize the findings into one paragraph.\nNotes:\n{prev}", 150),
        ],
    },
    "cheap": {
        "name": {"zh": "全小模型流水线(成本对照)", "en": "All-small pipeline (cost control)"},
        "steps": [
            ({"zh": "规划", "en": "Plan"}, "qwen3.5-2b", "Plan, in 3 numbered steps, how to add JWT auth to a Go HTTP API.", 120),
            ({"zh": "执行 1", "en": "Act 1"}, "qwen3.5-2b", "Execute step 1. Reply with only code.\nPlan:\n{plan}", 120),
            ({"zh": "执行 2", "en": "Act 2"}, "qwen3.5-2b", "Execute step 2. Reply with only code.\nPlan:\n{plan}", 120),
            ({"zh": "总结", "en": "Summarize"}, "qwen3.5-4b", "Summarize in two sentences.\nPlan:\n{plan}", 90),
        ],
    },
}

RUNS = {}
RUNS_LOCK = threading.Lock()


def run_agent(task_id, scenario_key):
    sc = SCENARIOS[scenario_key]
    plan = prev = ""
    for i, (_, model, tmpl, max_tokens) in enumerate(sc["steps"]):
        with RUNS_LOCK:
            RUNS[task_id]["steps"][i]["status"] = "running"
        try:
            text, tokens = _chat(task_id, model, tmpl.format(plan=plan, prev=prev), max_tokens)
            if i == 0:
                plan = text
            prev = text
            with RUNS_LOCK:
                RUNS[task_id]["steps"][i].update(
                    status="done", tokens=tokens,
                    snippet=text.strip().replace("\n", " ")[:110])
        except Exception as e:  # noqa: BLE001
            with RUNS_LOCK:
                RUNS[task_id]["steps"][i].update(status="error", snippet=str(e)[:110])
                RUNS[task_id]["status"] = "error"
            return
        time.sleep(1.5)
    with RUNS_LOCK:
        RUNS[task_id]["status"] = "done"


def start_run(scenario_key):
    sc = SCENARIOS[scenario_key]
    task_id = f"panel-{scenario_key}-{int(time.time()) % 100000}"
    with RUNS_LOCK:
        if any(r["status"] == "running" for r in RUNS.values()):
            return None, "busy"
        RUNS[task_id] = {
            "task_id": task_id, "scenario": scenario_key, "name": sc["name"],
            "status": "running", "started": time.time(),
            "steps": [{"title": t, "model": m, "status": "pending", "tokens": 0, "snippet": ""}
                      for (t, m, _, _) in sc["steps"]],
        }
    threading.Thread(target=run_agent, args=(task_id, scenario_key), daemon=True).start()
    return task_id, None


class Handler(BaseHTTPRequestHandler):
    def _json(self, obj, code=200):
        body = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path == "/api/run":
            n = int(self.headers.get("Content-Length", 0))
            try:
                req = json.loads(self.rfile.read(n) or b"{}")
            except json.JSONDecodeError:
                req = {}
            key = req.get("scenario")
            if key not in SCENARIOS:
                self._json({"error": f"unknown scenario {key!r}"}, 400)
                return
            tid, err = start_run(key)
            self._json({"error": err} if err else {"task_id": tid}, 409 if err else 200)
            return
        self._json({"error": "not found"}, 404)

    def do_GET(self):
        if self.path == "/api/runs":
            with RUNS_LOCK:
                runs = sorted(RUNS.values(), key=lambda r: -r["started"])
            known = {r["task_id"] for r in runs}
            try:
                with urllib.request.urlopen(GATEWAY + "/tasks", timeout=5) as r:
                    for t in json.load(r):
                        if t["id"] not in known:
                            runs.append({"task_id": t["id"], "scenario": "external",
                                         "name": {"zh": "外部任务(沙箱/agentsvc)",
                                                  "en": "External task (sandbox/agentsvc)"},
                                         "status": "done", "started": 0, "steps": []})
            except Exception:  # noqa: BLE001
                pass
            self._json(runs)
            return
        if self.path.startswith("/api/run/"):
            task_id = self.path[len("/api/run/"):]
            with RUNS_LOCK:
                run = RUNS.get(task_id)
            self._json(run if run else {"error": "unknown run"}, 200 if run else 404)
            return
        if self.path.startswith("/api/tasks/"):
            try:
                with urllib.request.urlopen(GATEWAY + self.path[len("/api"):], timeout=8) as r:
                    body = r.read()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(body)
            except Exception as e:  # noqa: BLE001
                self._json({"error": str(e)}, 502)
            return
        page = (ROOT / "index.html").read_bytes()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.end_headers()
        self.wfile.write(page)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    print("agent demo panel listening on :8080", flush=True)
    ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
