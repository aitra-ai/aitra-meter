// Sandbox agent app — what an xModeling Space runs. Serves a small web UI on
// :80; the user gives it a goal and it executes a plan→act→review loop against
// the LLM endpoint injected by the sandbox launcher (OPENAI_BASE_URL, which
// carries this sandbox's task id in its path — that is the entire metering
// integration).
const http = require("http");

const BASE = process.env.OPENAI_BASE_URL || "";
const SANDBOX_ID = process.env.SANDBOX_ID || "unknown";

let run = null; // {goal, status, steps:[{title,model,status,snippet,tokens}]}

async function chat(model, prompt, maxTokens) {
  const r = await fetch(`${BASE}/chat/completions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      model,
      messages: [{ role: "user", content: prompt }],
      max_tokens: maxTokens,
      temperature: 0.7,
    }),
  });
  if (!r.ok) throw new Error(`LLM HTTP ${r.status}`);
  const body = await r.json();
  return {
    text: body.choices[0].message.content,
    tokens: (body.usage || {}).completion_tokens || 0,
  };
}

async function agentLoop(goal) {
  const steps = [
    { title: "规划", model: "qwen3.6-27b", prompt: () => `You are an agent. Plan, in 3 short numbered steps, how to accomplish: ${goal}`, max: 150 },
    { title: "执行 1", model: "qwen3.5-2b", prompt: (p) => `Execute step 1 concisely.\nGoal: ${goal}\nPlan:\n${p}`, max: 120 },
    { title: "执行 2", model: "qwen3.5-2b", prompt: (p) => `Execute step 2 concisely.\nGoal: ${goal}\nPlan:\n${p}`, max: 120 },
    { title: "执行 3", model: "qwen3.5-2b", prompt: (p) => `Execute step 3 concisely.\nGoal: ${goal}\nPlan:\n${p}`, max: 120 },
    { title: "评审", model: "qwen3.5-122b-a10b-int4", prompt: (p) => `Review the plan in 2 bullets:\n${p}`, max: 120 },
    { title: "总结", model: "qwen3.5-9b", prompt: (p) => `Summarize the work in two sentences.\nGoal: ${goal}\nPlan:\n${p}`, max: 90 },
  ];
  run = { goal, status: "running", steps: steps.map((s) => ({ title: s.title, model: s.model, status: "pending", snippet: "", tokens: 0 })) };
  let plan = "";
  for (let i = 0; i < steps.length; i++) {
    run.steps[i].status = "running";
    try {
      const { text, tokens } = await chat(steps[i].model, steps[i].prompt(plan), steps[i].max);
      if (i === 0) plan = text;
      run.steps[i].status = "done";
      run.steps[i].snippet = text.trim().replace(/\s+/g, " ").slice(0, 110);
      run.steps[i].tokens = tokens;
    } catch (e) {
      run.steps[i].status = "error";
      run.steps[i].snippet = String(e).slice(0, 110);
      run.status = "error";
      return;
    }
    await new Promise((res) => setTimeout(res, 1200));
  }
  run.status = "done";
}

const PAGE = `<!doctype html><html lang="zh"><head><meta charset="utf-8">
<title>沙箱 Agent — ${SANDBOX_ID}</title><style>
 body{background:#101623;color:#e6ebf5;font:14px/1.6 -apple-system,"PingFang SC",sans-serif;max-width:760px;margin:0 auto;padding:30px}
 h1{font-size:18px}.sub{color:#8b96ad;font-size:12.5px;margin-bottom:18px}
 form{display:flex;gap:8px;margin-bottom:20px}
 input{flex:1;background:#171e2e;border:1px solid #26304a;border-radius:8px;color:#e6ebf5;padding:10px 12px;font-size:14px}
 button{background:#2f6fed;border:0;border-radius:8px;color:#fff;padding:10px 18px;font-size:14px;cursor:pointer}
 button:disabled{opacity:.5}
 li{list-style:none;display:flex;gap:10px;align-items:center;padding:8px 0;border-bottom:1px solid #26304a;font-size:13px}
 .dot{width:10px;height:10px;border-radius:50%;background:#3a4560;flex:none}
 .dot.running{background:#f5a623;animation:p 1s infinite}.dot.done{background:#3ecf8e}.dot.error{background:#f06767}
 @keyframes p{50%{opacity:.3}}
 .m{color:#4ea1ff;font-size:12px;width:165px;flex:none}.t{width:56px;font-weight:600;flex:none}
 .s{color:#8b96ad;font-size:12px;overflow:hidden;white-space:nowrap;text-overflow:ellipsis;flex:1}
 .tk{color:#8b96ad;font-size:12px;flex:none}
 .hint{color:#8b96ad;font-size:12px;margin-top:16px}
</style></head><body>
<h1>🧪 沙箱 Agent<span style="color:#8b96ad;font-weight:400"> · ${SANDBOX_ID}</span></h1>
<div class="sub">这是一个 xModeling 式沙箱容器 · LLM 出口由启动器注入(OPENAI_BASE_URL 带任务身份)· 能耗账单见 meter 面板</div>
<form onsubmit="go(event)"><input id="goal" placeholder="给 agent 一个目标,例如:为博客系统设计评论功能" required>
<button id="btn">执行</button></form>
<ul id="steps"></ul><div class="hint" id="hint"></div>
<script>
async function go(e){e.preventDefault();document.getElementById("btn").disabled=true;
 await fetch("/run",{method:"POST",headers:{"Content-Type":"application/json"},
   body:JSON.stringify({goal:document.getElementById("goal").value})});}
setInterval(async()=>{const r=await(await fetch("/status")).json();if(!r)return;
 document.getElementById("steps").innerHTML=r.steps.map(s=>
  '<li><span class="dot '+s.status+'"></span><span class="t">'+s.title+'</span><span class="m">'+s.model+
  '</span><span class="s">'+(s.snippet||"")+'</span><span class="tk">'+(s.tokens?("+"+s.tokens+" tok"):"")+'</span></li>').join("");
 document.getElementById("btn").disabled = r.status==="running";
 document.getElementById("hint").textContent = r.status==="done"?"完成 — 到 meter 面板查看本任务能耗账单":"";
},1000);
</script></body></html>`;

http.createServer((req, res) => {
  if (req.method === "POST" && req.url === "/run") {
    let body = "";
    req.on("data", (c) => (body += c));
    req.on("end", () => {
      if (!run || run.status !== "running") {
        const goal = (JSON.parse(body || "{}").goal || "demo").slice(0, 300);
        agentLoop(goal);
      }
      res.writeHead(202, { "Content-Type": "application/json" });
      res.end("{}");
    });
    return;
  }
  if (req.url === "/status") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(run));
    return;
  }
  res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
  res.end(PAGE);
}).listen(80, () => console.log(`sandbox agent up, base=${BASE}`));
