"use client";

import { useMetrics } from "@/lib/useMetrics";
import { parseValue } from "@/lib/prometheus";

type Agg = "first" | "sum" | "avg" | "max";

interface Def {
  metric: string;
  label: string;
  unit: string;
  agg?: Agg;
}

// Every aitra_* metric the aggregation service emits, grouped for readability.
const GROUPS: { title: string; items: Def[] }[] = [
  {
    title: "Efficiency",
    items: [
      { metric: "aitra_j_per_token", label: "Energy / token", unit: "J/tok" },
      { metric: "aitra_system_j_per_token", label: "System energy / token", unit: "J/tok" },
      { metric: "aitra_tokens_per_joule", label: "Tokens / joule", unit: "tok/J" },
      { metric: "aitra_model_energy_per_1m_tokens", label: "Energy / 1M tokens", unit: "J" },
      { metric: "aitra_gpu_utilization_efficiency", label: "GPU utilization efficiency", unit: "ratio" },
    ],
  },
  {
    title: "Power",
    items: [
      { metric: "aitra_gpu_power_watts", label: "GPU power", unit: "W", agg: "avg" },
      { metric: "aitra_idle_power_watts", label: "Idle power", unit: "W", agg: "avg" },
      { metric: "aitra_host_power_watts", label: "Host power", unit: "W", agg: "avg" },
    ],
  },
  {
    title: "Utilization",
    items: [
      { metric: "aitra_gpu_serving_utilization_ratio", label: "GPU serving utilization", unit: "ratio" },
      { metric: "aitra_idle_time_ratio", label: "Idle time", unit: "ratio" },
      { metric: "aitra_host_energy_fraction", label: "Host energy fraction", unit: "ratio" },
    ],
  },
  {
    title: "Throughput",
    items: [
      { metric: "aitra_model_tokens_total", label: "Model tokens", unit: "tokens", agg: "sum" },
      { metric: "aitra_namespace_tokens_total", label: "Namespace tokens", unit: "tokens", agg: "sum" },
    ],
  },
  {
    title: "Energy",
    items: [
      { metric: "aitra_namespace_energy_joules_total", label: "Namespace energy", unit: "J", agg: "sum" },
      { metric: "aitra_host_energy_joules_total", label: "Host energy", unit: "J", agg: "sum" },
    ],
  },
  {
    title: "Cost",
    items: [
      { metric: "aitra_cost_per_million_tokens_usd", label: "Cost / 1M tokens", unit: "USD" },
      { metric: "aitra_model_cost_per_1m_tokens_usd", label: "Model cost / 1M tokens", unit: "USD" },
      { metric: "aitra_tenant_cost_usd_total", label: "Tenant cost", unit: "USD", agg: "sum" },
    ],
  },
  {
    title: "Carbon",
    items: [{ metric: "aitra_co2_per_token_grams", label: "CO₂ / token", unit: "g" }],
  },
  {
    title: "Measurement quality",
    items: [
      { metric: "aitra_measurement_cv", label: "Coefficient of variation", unit: "" },
      { metric: "aitra_measurement_window_stable", label: "Window stable", unit: "bool" },
    ],
  },
];

function format(v: number, unit: string): string {
  if (unit === "bool") return v >= 1 ? "stable" : "unstable";
  if (unit === "ratio") return (v * 100).toFixed(1) + "%";
  if (unit === "USD") return v < 1 ? "$" + v.toFixed(4) : "$" + v.toFixed(2);
  if (Math.abs(v) >= 1e6) return (v / 1e6).toFixed(2) + "M";
  if (Math.abs(v) >= 1e3) return (v / 1e3).toFixed(2) + "k";
  if (Number.isInteger(v)) return v.toString();
  return v.toFixed(Math.abs(v) < 1 ? 4 : 2);
}

function MetricCard({ def }: { def: Def }) {
  const { data, error, isLoading } = useMetrics(def.metric);

  let value: number | null = null;
  if (data && data.length) {
    const nums = data.map((r) => parseValue(r.value[1])).filter((n) => !Number.isNaN(n));
    if (nums.length) {
      const agg = def.agg ?? "first";
      value =
        agg === "sum" ? nums.reduce((a, b) => a + b, 0)
        : agg === "avg" ? nums.reduce((a, b) => a + b, 0) / nums.length
        : agg === "max" ? Math.max(...nums)
        : nums[0];
    }
  }

  const display = error ? "—" : value === null ? (isLoading ? "…" : "n/a") : format(value, def.unit);
  const showUnit =
    value !== null && !error && def.unit && def.unit !== "bool" && def.unit !== "ratio" && def.unit !== "USD";

  return (
    <div className="rounded-lg border border-gray-200 bg-white p-3 shadow-sm">
      <div className="text-xs text-gray-500">{def.label}</div>
      <div className="mt-1 text-lg font-semibold text-gray-900">
        {display}
        {showUnit && <span className="ml-1 text-xs font-normal text-gray-400">{def.unit}</span>}
      </div>
      <div className="mt-1 truncate font-mono text-[10px] text-gray-300" title={def.metric}>
        {def.metric}
      </div>
    </div>
  );
}

export function MetricsOverview() {
  return (
    <div className="space-y-6">
      {GROUPS.map((g) => (
        <div key={g.title}>
          <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-gray-400">{g.title}</h3>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
            {g.items.map((it) => (
              <MetricCard key={it.metric} def={it} />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
