"use client";

import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
  ResponsiveContainer,
} from "recharts";
import { useMetricsRange } from "@/lib/useMetricsRange";
import { useMetrics } from "@/lib/useMetrics";
import { parseValue } from "@/lib/prometheus";

// ---- faithful mirror of the provisioned Grafana "Aitra Meter" dashboard ----

type Unit = "short" | "none" | "watt" | "currencyUSD" | "percentunit";

interface Target {
  expr: string;
  legend: string; // Grafana legendFormat, e.g. "GPU power — {{node}}"
}

interface Panel {
  title: string;
  unit: Unit;
  targets: Target[];
  dualAxis?: boolean; // second target on right axis when scales differ wildly
}

interface Row {
  ts: number;
  [key: string]: number;
}

// Grafana's panel 1 pairs J/token with nvidia_gpu_utilization, which isn't
// scraped on this cluster (aitra-meter reads the GPU via NVML in-process rather
// than a dcgm/nvidia exporter). aitra_gpu_serving_utilization_ratio is the
// available equivalent, kept on a right axis since it's a 0–1 ratio next to
// ~10 J/token.
const PANELS: Panel[] = [
  {
    title: "J/token vs GPU utilisation",
    unit: "short",
    dualAxis: true,
    targets: [
      { expr: "aitra_j_per_token", legend: "J/token — {{model}} {{hardware}}" },
      { expr: "aitra_gpu_serving_utilization_ratio", legend: "GPU util — {{node}}" },
    ],
  },
  {
    title: "Tokens per joule",
    unit: "short",
    targets: [{ expr: "aitra_tokens_per_joule", legend: "tokens/J — {{namespace}} {{model}}" }],
  },
  {
    title: "Cost per million tokens (USD)",
    unit: "currencyUSD",
    targets: [{ expr: "aitra_cost_per_million_tokens_usd", legend: "{{namespace}} {{model}}" }],
  },
  {
    title: "GPU power (W) — live",
    unit: "watt",
    targets: [
      { expr: "aitra_gpu_power_watts", legend: "GPU power — {{node}}" },
      { expr: "aitra_idle_power_watts", legend: "idle floor — {{node}}" },
    ],
  },
  {
    title: "Host (spbm) vs GPU power — non-accelerator energy (#82)",
    unit: "watt",
    targets: [
      { expr: "aitra_gpu_power_watts", legend: "GPU {{model_name}}" },
      { expr: "aitra_host_power_watts", legend: "host CPU (cpu_e+cpu_p)" },
    ],
  },
  {
    title: "System vs GPU J/token  (host fraction = non-GPU share)",
    unit: "none",
    targets: [
      { expr: "aitra_system_j_per_token", legend: "system (gpu+host) {{model_name}}" },
      { expr: "aitra_j_per_token", legend: "gpu-only {{model_name}}" },
    ],
  },
];

interface GaugeDef {
  title: string;
  metric: string;
  legend: string;
  min: number;
  max: number;
  steps: { color: string; at: number }[]; // ascending; color applies at/above `at`
}

const GAUGES: GaugeDef[] = [
  {
    title: "Idle time ratio",
    metric: "aitra_idle_time_ratio",
    legend: "{{node}}",
    min: 0,
    max: 1,
    steps: [
      { color: "#16a34a", at: 0 },
      { color: "#d97706", at: 0.3 },
      { color: "#dc2626", at: 0.4 },
    ],
  },
  {
    title: "Measurement CV",
    metric: "aitra_measurement_cv",
    legend: "{{node}} {{model_name}}",
    min: 0,
    max: 0.1,
    steps: [
      { color: "#16a34a", at: 0 },
      { color: "#d97706", at: 0.02 },
      { color: "#dc2626", at: 0.03 },
    ],
  },
];

const PALETTE = ["#2563eb", "#dc2626", "#16a34a", "#d97706", "#7c3aed", "#0891b2", "#be185d", "#65a30d"];

function formatLegend(tpl: string, m: Record<string, string>): string {
  return tpl
    .replace(/\{\{\s*(\w+)\s*\}\}/g, (_, k) => m[k] ?? "")
    .replace(/\s+/g, " ")
    .replace(/—\s*$/, "")
    .trim();
}

function axisFmt(unit: Unit) {
  return (v: number): string => {
    if (unit === "watt") return `${v.toFixed(0)}`;
    if (unit === "currencyUSD") return v < 1 ? `$${v.toFixed(2)}` : `$${v.toFixed(0)}`;
    if (unit === "percentunit") return `${(v * 100).toFixed(0)}%`;
    return v >= 1000 ? `${(v / 1000).toFixed(1)}k` : `${v}`;
  };
}

function tipFmt(unit: Unit) {
  return (v: number): string => {
    if (unit === "watt") return `${v.toFixed(1)} W`;
    if (unit === "currencyUSD") return `$${v.toFixed(v < 1 ? 4 : 2)}`;
    if (unit === "percentunit") return `${(v * 100).toFixed(1)}%`;
    return v.toFixed(4);
  };
}

function fmtTime(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

function TimeseriesPanel({ panel }: { panel: Panel }) {
  const now = Math.floor(Date.now() / 1000);
  const start = now - 3600;
  const step = "60s";

  const t0 = panel.targets[0];
  const t1 = panel.targets[1];
  const r0 = useMetricsRange({ query: t0.expr, start, end: now, step });
  const r1 = useMetricsRange({ query: t1 ? t1.expr : t0.expr, start, end: now, step });

  const lines: { key: string; axis: "left" | "right" }[] = [];
  const map = new Map<number, Row>();

  const addTarget = (
    res: { data?: { metric: Record<string, string>; values: [number, string][] }[] },
    tgt: Target | undefined,
    axis: "left" | "right",
  ) => {
    if (!tgt || !res.data) return;
    res.data.forEach((s) => {
      const key = formatLegend(tgt.legend, s.metric) || tgt.expr;
      if (!lines.some((l) => l.key === key)) lines.push({ key, axis });
      for (const [ts, val] of s.values) {
        const t = ts * 1000;
        const row = map.get(t) ?? { ts: t };
        row[key] = parseValue(val);
        map.set(t, row);
      }
    });
  };

  addTarget(r0, t0, "left");
  addTarget(r1, t1, panel.dualAxis ? "right" : "left");

  const data = Array.from(map.values()).sort((a, b) => a.ts - b.ts);
  const err = r0.error || (t1 && r1.error);
  const empty = data.length === 0;

  return (
    <div className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm">
      <h4 className="mb-2 text-sm font-medium text-gray-700">{panel.title}</h4>
      <div className="h-56">
        {err ? (
          <div className="flex h-full items-center justify-center text-xs text-red-500">error loading</div>
        ) : empty ? (
          <div className="flex h-full items-center justify-center text-xs text-gray-400">no data yet</div>
        ) : (
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={data} margin={{ top: 5, right: 12, left: 0, bottom: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="ts" tickFormatter={fmtTime} tick={{ fontSize: 11 }} minTickGap={40} />
              <YAxis yAxisId="left" tick={{ fontSize: 11 }} width={48} tickFormatter={axisFmt(panel.unit)} />
              {panel.dualAxis && (
                <YAxis
                  yAxisId="right"
                  orientation="right"
                  tick={{ fontSize: 11 }}
                  width={44}
                  tickFormatter={axisFmt("percentunit")}
                />
              )}
              <Tooltip
                labelFormatter={(v) => fmtTime(Number(v))}
                formatter={(val, name) => {
                  const num = typeof val === "number" ? val : parseFloat(String(val));
                  const ln = lines.find((l) => l.key === String(name));
                  const text = ln?.axis === "right" ? tipFmt("percentunit")(num) : tipFmt(panel.unit)(num);
                  return [text, String(name)];
                }}
              />
              <Legend wrapperStyle={{ fontSize: 10 }} />
              {lines.map((ln, i) => (
                <Line
                  key={ln.key}
                  yAxisId={ln.axis}
                  type="monotone"
                  dataKey={ln.key}
                  stroke={PALETTE[i % PALETTE.length]}
                  dot={false}
                  strokeWidth={2}
                  isAnimationActive={false}
                  connectNulls
                />
              ))}
            </LineChart>
          </ResponsiveContainer>
        )}
      </div>
    </div>
  );
}

function GaugePanel({ gauge }: { gauge: GaugeDef }) {
  const { data, error } = useMetrics(gauge.metric);
  const series = data && data.length ? data[0] : null;
  const v = series ? parseValue(series.value[1]) : null;

  // threshold color: highest step whose `at` <= value
  let color = gauge.steps[0].color;
  if (v !== null) for (const s of gauge.steps) if (v >= s.at) color = s.color;

  const pct = v === null ? 0 : Math.max(0, Math.min(100, ((v - gauge.min) / (gauge.max - gauge.min)) * 100));
  const label = series ? formatLegend(gauge.legend, series.metric) : "";

  return (
    <div className="rounded-lg border border-gray-200 bg-white p-4 shadow-sm">
      <h4 className="mb-2 text-sm font-medium text-gray-700">{gauge.title}</h4>
      <div className="text-2xl font-bold" style={{ color: v === null ? "#9ca3af" : color }}>
        {error ? "—" : v === null ? "…" : `${(v * 100).toFixed(1)}%`}
      </div>
      <div className="mt-2 h-2.5 w-full overflow-hidden rounded-full bg-gray-100">
        <div className="h-full rounded-full transition-all" style={{ width: `${pct}%`, background: color }} />
      </div>
      <div className="mt-1 flex justify-between text-[10px] text-gray-400">
        <span>{gauge.min}</span>
        <span className="truncate">{label}</span>
        <span>{gauge.max}</span>
      </div>
    </div>
  );
}

export function GrafanaGraphs() {
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {PANELS.map((p) => (
          <TimeseriesPanel key={p.title} panel={p} />
        ))}
      </div>
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        {GAUGES.map((g) => (
          <GaugePanel key={g.title} gauge={g} />
        ))}
      </div>
    </div>
  );
}
