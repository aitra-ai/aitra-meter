// booth-agent — the xModeling agent experience, standalone for booth visitors.
// Same engine (CCB headless, stream-json), same isolation model as agentsvc
// (one process + private workdir per session), same permission protocol
// (stdio approvals against a tool whitelist). Model calls ride through the
// aitra-meter task-gateway, so every visitor session accrues an energy bill.
const http = require("http");
const fs = require("fs");
const path = require("path");
const { spawn } = require("child_process");
const readline = require("readline");

const PORT = 80;
const CCB = process.env.CCB_ENTRY || "/ccb/cli.js";
// In-cluster default; when running as a host-level docker container, set
// TASK_GW to the gateway NodePort (e.g. http://<node-ip>:30854).
const GATEWAY = process.env.TASK_GW || "http://aitra-meter-task-gateway.aitra-system:8090";
// Models the visitor may pick — every entry must have vLLM tool calling
// enabled (--enable-auto-tool-choice --tool-call-parser qwen3_xml) and a
// context window that fits CCB's system prompt (32k).
const MODELS = (process.env.AGENT_MODELS || "qwen3.6-27b,qwen3.5-9b,qwen3.5-4b").split(",");
const DEFAULT_MODEL = MODELS[0];
const WORK_ROOT = "/work";
const MAX_SESSIONS = 4;
const SESSION_TTL_MS = 15 * 60 * 1000;
const MAX_TURNS = "14";
const ALLOW = new Set(["Bash", "Write", "Read", "Edit", "MultiEdit", "LS", "Glob", "Grep", "TodoWrite"]);

const sessions = new Map(); // sid -> {proc, events:[], subs:Set<res>, lastSeen, status}

function push(s, ev) {
  ev.seq = s.events.length;
  s.events.push(ev);
  const line = `id: ${ev.seq}\ndata: ${JSON.stringify(ev)}\n\n`;
  for (const res of s.subs) res.write(line);
}

function newSession(model) {
  if (!MODELS.includes(model)) model = DEFAULT_MODEL;
  const alive = [...sessions.values()].filter(x => x.status === "running").length;
  if (alive >= MAX_SESSIONS) return null;
  const sid = "visitor-" + Math.random().toString(36).slice(2, 8);
  const dir = path.join(WORK_ROOT, sid);
  fs.mkdirSync(path.join(dir, "cfg"), { recursive: true });

  const proc = spawn("node", [
    CCB, "-p",
    "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
    "--permission-prompt-tool", "stdio", "--max-turns", MAX_TURNS,
    "--disallowedTools", "WebSearch", "WebFetch", "artifact", "VaultHttpFetch", "Task",
  ], {
    cwd: dir,
    env: {
      PATH: process.env.PATH, HOME: dir,
      CLAUDE_CONFIG_DIR: path.join(dir, "cfg"),
      CLAUDE_CODE_USE_OPENAI: "1",
      OPENAI_BASE_URL: `${GATEWAY}/t/${sid}/v1`,
      OPENAI_API_KEY: "booth",
      OPENAI_MODEL: model,
      OPENAI_SMALL_FAST_MODEL: model,
      CLAUDE_CODE_MAX_OUTPUT_TOKENS: "4096",
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
      DISABLE_TELEMETRY: "1", DISABLE_AUTOUPDATER: "1",
      DISABLE_ERROR_REPORTING: "1", DISABLE_BUG_COMMAND: "1",
    },
    stdio: ["pipe", "pipe", "ignore"],
  });

  const s = { sid, proc, events: [], subs: new Set(), lastSeen: Date.now(), status: "running", model };
  sessions.set(sid, s);

  const send = (o) => proc.stdin.write(JSON.stringify(o) + "\n");
  readline.createInterface({ input: proc.stdout }).on("line", (line) => {
    if (!line.trim()) return;
    let o; try { o = JSON.parse(line); } catch { return; }

    if (o.type === "control_request" && o.request?.subtype === "can_use_tool") {
      const name = o.request.tool_name;
      const ok = ALLOW.has(name);
      send({ type: "control_response", response: {
        subtype: "success", request_id: o.request_id,
        response: ok ? { behavior: "allow", updatedInput: o.request.input }
                     : { behavior: "deny", message: "tool not allowed in the booth sandbox" },
      }});
      push(s, { t: "perm", tool: name, allowed: ok });
      return;
    }
    if (o.type === "system" && o.subtype === "init") {
      push(s, { t: "init", model: o.model });
      return;
    }
    if (o.type === "assistant" && o.message) {
      for (const b of o.message.content || []) {
        if (b.type === "text" && b.text?.trim()) push(s, { t: "text", text: b.text });
        if (b.type === "tool_use") push(s, { t: "tool.call", name: b.name, input: JSON.stringify(b.input).slice(0, 400) });
      }
      return;
    }
    if (o.type === "user" && o.message) {
      for (const b of o.message.content || []) {
        if (b.type === "tool_result") {
          const out = typeof b.content === "string" ? b.content : JSON.stringify(b.content);
          push(s, { t: "tool.result", error: !!b.is_error, output: String(out).slice(0, 600) });
        }
      }
      return;
    }
    if (o.type === "result") {
      push(s, { t: "turn.end", turns: o.num_turns });
    }
  });
  proc.on("exit", () => {
    s.status = "closed";
    push(s, { t: "closed" });
  });
  return s;
}

function reap() {
  const now = Date.now();
  for (const s of sessions.values()) {
    if (s.status === "running" && now - s.lastSeen > SESSION_TTL_MS) {
      try { s.proc.kill(); } catch {}
      s.status = "expired";
      push(s, { t: "expired" });
    }
  }
}
setInterval(reap, 30_000);

function json(res, obj, code = 200) {
  const b = JSON.stringify(obj);
  res.writeHead(code, { "Content-Type": "application/json; charset=utf-8" });
  res.end(b);
}

const PAGE = fs.readFileSync(path.join(__dirname, "index.html"));

http.createServer(async (req, res) => {
  const [p, q] = req.url.split("?");
  if (req.method === "POST" && p === "/api/session") {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      let model = DEFAULT_MODEL;
      try { model = String(JSON.parse(body || "{}").model || DEFAULT_MODEL); } catch {}
      const s = newSession(model);
      if (!s) { json(res, { error: "busy" }, 429); return; }
      json(res, { sid: s.sid, model: s.model });
    });
    return;
  }
  if (req.method === "GET" && p === "/api/models") {
    json(res, { models: MODELS, default: DEFAULT_MODEL });
    return;
  }
  const m = p.match(/^\/api\/session\/([a-z0-9-]+)\/(msg|events|bill|info)$/);
  if (m) {
    const s = sessions.get(m[1]);
    if (!s) { json(res, { error: "unknown session" }, 404); return; }
    s.lastSeen = Date.now();
    if (m[2] === "msg" && req.method === "POST") {
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", () => {
        let text = "";
        try { text = String(JSON.parse(body).text || "").slice(0, 2000); } catch {}
        if (!text || s.status !== "running") { json(res, { error: "bad" }, 400); return; }
        s.proc.stdin.write(JSON.stringify({ type: "user",
          message: { role: "user", content: [{ type: "text", text }] } }) + "\n");
        push(s, { t: "user", text });
        json(res, { ok: true });
      });
      return;
    }
    if (m[2] === "events") {
      res.writeHead(200, { "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache", Connection: "keep-alive" });
      const after = Number(new URLSearchParams(q || "").get("after") ?? -1);
      for (const ev of s.events) if (ev.seq > after) res.write(`id: ${ev.seq}\ndata: ${JSON.stringify(ev)}\n\n`);
      s.subs.add(res);
      req.on("close", () => s.subs.delete(res));
      return;
    }
    if (m[2] === "info") {
      json(res, { sid: s.sid, status: s.status, events: s.events.length, model: s.model });
      return;
    }
    if (m[2] === "bill") {
      try {
        const r = await fetch(`${GATEWAY}/tasks/${s.sid}`);
        res.writeHead(r.status, { "Content-Type": "application/json" });
        res.end(await r.text());
      } catch (e) { json(res, { error: String(e) }, 502); }
      return;
    }
  }
  res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
  res.end(PAGE);
}).listen(PORT, () => console.log(`booth agent up :${PORT} models=${MODELS.join(",")} gw=${GATEWAY}`));
