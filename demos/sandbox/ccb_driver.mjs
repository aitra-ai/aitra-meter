// CCB headless driver — the agentsvc pattern in miniature. Spawns CCB with
// stdio permission prompts and auto-approves ONLY whitelisted local tools
// (file + shell inside the container workdir); everything else is denied.
// The container is the sandbox; this driver is the permission gate.
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";

const PROMPT = process.env.TASK_PROMPT || "Say hi.";
const ALLOW = new Set(["Bash", "Write", "Read", "Edit", "MultiEdit", "LS", "Glob", "Grep", "TodoWrite"]);

const child = spawn("node", [
  "/ccb/cli.js", "-p",
  "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
  "--permission-prompt-tool", "stdio", "--max-turns", "14",
  "--disallowedTools", "WebSearch", "WebFetch", "artifact", "VaultHttpFetch", "Task",
], { stdio: ["pipe", "pipe", "inherit"] });

const send = (o) => child.stdin.write(JSON.stringify(o) + "\n");
send({ type: "user", message: { role: "user", content: [{ type: "text", text: PROMPT }] } });

let tools = 0, denials = 0;
createInterface({ input: child.stdout }).on("line", (line) => {
  if (!line.trim()) return;
  let o; try { o = JSON.parse(line); } catch { return; }

  if (o.type === "control_request" && o.request?.subtype === "can_use_tool") {
    const name = o.request.tool_name;
    const ok = ALLOW.has(name);
    if (ok) tools++; else denials++;
    console.log(`[perm] ${name} -> ${ok ? "allow" : "deny"}`);
    send({ type: "control_response", response: {
      subtype: "success", request_id: o.request_id,
      response: ok
        ? { behavior: "allow", updatedInput: o.request.input }
        : { behavior: "deny", message: "tool not in the sandbox whitelist" },
    }});
    return;
  }
  if (o.type === "system" && o.subtype === "init") {
    console.log(`[init] session=${o.session_id} model=${o.model}`);
    return;
  }
  if (o.type === "assistant" && o.message) {
    for (const b of o.message.content || []) {
      if (b.type === "tool_use") console.log(`[tool.call] ${b.name} ${JSON.stringify(b.input).slice(0, 120)}`);
      if (b.type === "text" && b.text?.trim()) console.log(`[text] ${b.text.trim().slice(0, 160)}`);
    }
    return;
  }
  if (o.type === "user" && o.message) {
    for (const b of o.message.content || []) {
      if (b.type === "tool_result") {
        const out = typeof b.content === "string" ? b.content : JSON.stringify(b.content);
        console.log(`[tool.result] err=${!!b.is_error} ${String(out).replace(/\n/g, " ").slice(0, 120)}`);
      }
    }
    return;
  }
  if (o.type === "result") {
    console.log(`[result] subtype=${o.subtype} turns=${o.num_turns} tools_approved=${tools} denied=${denials}`);
    console.log(`RESULT_JSON ${JSON.stringify({ subtype: o.subtype, turns: o.num_turns, usage: o.usage || null })}`);
    child.stdin.end();
  }
});
child.on("exit", (code) => process.exit(code ?? 0));
