"use client";

import { useMetrics } from "@/lib/useMetrics";
import { parseValue } from "@/lib/prometheus";

// Queries run in parallel; SWR deduplicates concurrent fetches.
const Q_JPT = "aitra_j_per_token";
const Q_STABLE = "aitra_measurement_window_stable";
const Q_REF = "aitra_calibration_reference_j_per_token";

const TIER_BADGE: Record<string, string> = {
  aitra_benchmark: "bg-green-100 text-green-800",
  reference: "bg-blue-100 text-blue-800",
  self_calibrated: "bg-yellow-100 text-yellow-800",
  uncalibrated: "bg-gray-100 text-gray-600",
};

const TIER_LABEL: Record<string, string> = {
  aitra_benchmark: "Aitra",
  reference: "Ref",
  self_calibrated: "Self",
  uncalibrated: "—",
};

function TierBadge({ tier }: { tier: string }) {
  const cls = TIER_BADGE[tier] ?? "bg-gray-100 text-gray-600";
  const label = TIER_LABEL[tier] ?? tier;
  return (
    <span className={`inline-flex items-center rounded px-2 py-0.5 text-xs font-medium ${cls}`}>
      {label}
    </span>
  );
}

function EfficiencyDelta({
  jpt,
  ref,
}: {
  jpt: number;
  ref: number | undefined;
}) {
  if (ref == null || ref === 0) return <span className="text-gray-400">—</span>;
  const delta = ((jpt - ref) / ref) * 100;
  const sign = delta > 0 ? "+" : "";
  const cls =
    delta > 5
      ? "text-red-600"
      : delta < -5
        ? "text-green-600"
        : "text-gray-700";
  return (
    <span className={`font-mono text-sm ${cls}`}>
      {sign}{delta.toFixed(1)}%
    </span>
  );
}

function StatusDot({ stable }: { stable: boolean | undefined }) {
  if (stable == null)
    return <span className="h-2 w-2 rounded-full bg-gray-300 inline-block" />;
  return (
    <span
      className={`h-2 w-2 rounded-full inline-block ${stable ? "bg-green-500" : "bg-amber-400"}`}
      title={stable ? "Stable (CV < 3%)" : "Unstable (CV ≥ 3%)"}
    />
  );
}

export function JPerTokenTable() {
  const { data: jptData, error: jptErr, isLoading } = useMetrics(Q_JPT);
  const { data: stableData } = useMetrics(Q_STABLE);
  const { data: refData } = useMetrics(Q_REF);

  // Build lookup maps keyed by "model|hardware" for reference and "node|model" for stability.
  const refMap = new Map<string, number>();
  for (const r of refData ?? []) {
    const key = `${r.metric.model}|${r.metric.hardware}`;
    refMap.set(key, parseValue(r.value[1]));
  }

  const stableMap = new Map<string, boolean>();
  for (const r of stableData ?? []) {
    const key = `${r.metric.namespace}|${r.metric.model_name ?? r.metric.model}`;
    stableMap.set(key, parseValue(r.value[1]) === 1);
  }

  if (jptErr) {
    return (
      <div className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700">
        <strong>Could not reach Prometheus:</strong> {jptErr.message}
        <p className="mt-1 text-xs text-red-500">
          Check that PROMETHEUS_URL is set and the aggregation service is reachable.
        </p>
      </div>
    );
  }

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 text-sm text-gray-500">
        <span className="h-4 w-4 animate-spin rounded-full border-2 border-gray-300 border-t-blue-500" />
        Loading…
      </div>
    );
  }

  const rows = jptData ?? [];

  if (rows.length === 0) {
    return (
      <div data-testid="view-1-ready" className="rounded-md border border-dashed border-gray-300 p-8 text-center text-sm text-gray-500">
        No measurements yet. Deploy a vLLM workload and wait for the first measurement window.
      </div>
    );
  }

  return (
    <div data-testid="view-1-ready" className="overflow-x-auto rounded-lg border border-gray-200 shadow-sm">
      <table className="min-w-full divide-y divide-gray-200 text-sm">
        <thead className="bg-gray-50">
          <tr>
            {[
              "Namespace",
              "Workload",
              "Model",
              "Hardware",
              "Precision",
              "J / token",
              "vs Baseline",
              "Tier",
              "Attribution",
              "Stable",
            ].map((h) => (
              <th
                key={h}
                className="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wide text-gray-500"
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-100 bg-white">
          {rows.map((r, i) => {
            const m = r.metric;
            const jpt = parseValue(r.value[1]);
            const refKey = `${m.model}|${m.hardware}`;
            const ref = refMap.get(refKey);
            const stableKey = `${m.namespace}|${m.model}`;
            const stable = stableMap.get(stableKey);
            const isUnknownWorkload = !m.workload || m.workload === "unknown";

            return (
              <tr
                key={i}
                className={stable === false ? "bg-amber-50" : "hover:bg-gray-50"}
              >
                <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-gray-700">
                  {m.namespace}
                </td>
                <td className="whitespace-nowrap px-4 py-3">
                  {isUnknownWorkload ? (
                    <span className="italic text-gray-400">unknown</span>
                  ) : (
                    m.workload
                  )}
                </td>
                <td className="whitespace-nowrap px-4 py-3 font-medium text-gray-900">
                  {m.model}
                </td>
                <td className="whitespace-nowrap px-4 py-3 text-gray-600">
                  {m.hardware}
                </td>
                <td className="whitespace-nowrap px-4 py-3 text-gray-600">
                  {m.precision ?? "—"}
                </td>
                <td className="whitespace-nowrap px-4 py-3 font-mono font-semibold text-gray-900">
                  {jpt.toFixed(4)}
                </td>
                <td className="whitespace-nowrap px-4 py-3">
                  <EfficiencyDelta jpt={jpt} ref={ref} />
                </td>
                <td className="whitespace-nowrap px-4 py-3">
                  <TierBadge tier={m.calibration_tier ?? "uncalibrated"} />
                </td>
                <td className="whitespace-nowrap px-4 py-3 text-gray-600">
                  {m.attribution_method ?? "—"}
                </td>
                <td className="whitespace-nowrap px-4 py-3">
                  <StatusDot stable={stable} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <div className="border-t border-gray-100 bg-gray-50 px-4 py-2 text-xs text-gray-400">
        {rows.length} active combination{rows.length !== 1 ? "s" : ""} · refreshes every 15s
      </div>
    </div>
  );
}
