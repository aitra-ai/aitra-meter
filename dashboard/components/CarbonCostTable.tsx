"use client";

import { useState, useMemo } from "react";
import { useMetrics } from "@/lib/useMetrics";
import { parseValue } from "@/lib/prometheus";

// Instant vector: latest J/token per workload × model × hardware
const Q_JPT = "aitra_j_per_token";

// Grid intensity: gCO₂/kWh. Operator sets this metric via a ConfigMap scrape
// or a recording rule. Falls back to a user-adjustable input if absent.
const Q_GRID = "aitra_grid_intensity_gco2_per_kwh";

const SOURCE_BADGE: Record<string, string> = {
  live: "bg-green-100 text-green-800",
  operator_set: "bg-blue-100 text-blue-800",
  fallback: "bg-yellow-100 text-yellow-800",
};

// Derivation: cost_per_token = J/tok × PUE / 3,600,000 × cost$/kWh
//             co2_per_token  = J/tok × PUE / 3,600,000 × grid_intensity_g/kWh / 1000 (→ kg)
function deriveKwhPerToken(jpt: number, pue: number): number {
  return (jpt * pue) / 3_600_000;
}

export function CarbonCostTable() {
  const [pue, setPue] = useState(1.2);
  const [costPerKwh, setCostPerKwh] = useState(0.08);
  // Fallback grid intensity (used when the Prometheus metric is absent)
  const [fallbackGrid, setFallbackGrid] = useState(400); // gCO₂/kWh — EU avg

  const { data: jptData, error, isLoading } = useMetrics(Q_JPT);
  const { data: gridData } = useMetrics(Q_GRID);

  // Determine grid intensity source
  const { gridIntensity, gridSource } = useMemo(() => {
    if (gridData && gridData.length > 0) {
      const val = parseValue(gridData[0].value[1]);
      if (!isNaN(val) && val > 0) {
        const src = gridData[0].metric.source ?? "live";
        return { gridIntensity: val, gridSource: src };
      }
    }
    return { gridIntensity: fallbackGrid, gridSource: "fallback" };
  }, [gridData, fallbackGrid]);

  const rows = jptData ?? [];

  if (error) {
    return (
      <div className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700">
        <strong>Could not reach Prometheus:</strong> {error.message}
      </div>
    );
  }

  return (
    <div data-testid={!isLoading ? "view-5-ready" : undefined} className="space-y-4">
      {/* Controls */}
      <div className="flex flex-wrap items-end gap-6">
        <div className="min-w-48">
          <label className="mb-1 block text-xs font-medium text-gray-500">
            PUE{" "}
            <span className="font-mono text-gray-700">{pue.toFixed(2)}</span>
          </label>
          <input
            type="range"
            min={1.0}
            max={2.0}
            step={0.01}
            value={pue}
            onChange={(e) => setPue(parseFloat(e.target.value))}
            className="w-full accent-blue-600"
          />
          <div className="flex justify-between text-xs text-gray-400">
            <span>1.00</span>
            <span>2.00</span>
          </div>
        </div>

        <div>
          <label className="mb-1 block text-xs font-medium text-gray-500">
            Cost / kWh (USD)
          </label>
          <div className="flex items-center rounded-md border border-gray-300 bg-white px-2 py-1.5 text-sm">
            <span className="mr-1 text-gray-400">$</span>
            <input
              type="number"
              min={0}
              step={0.001}
              value={costPerKwh}
              onChange={(e) => setCostPerKwh(parseFloat(e.target.value) || 0)}
              className="w-16 focus:outline-none"
            />
          </div>
        </div>

        <div>
          <label className="mb-1 block text-xs font-medium text-gray-500">
            Grid intensity (gCO₂/kWh){" "}
            {gridSource !== "fallback" ? (
              <span className={`inline rounded px-1 py-0.5 text-xs font-medium ${SOURCE_BADGE[gridSource] ?? SOURCE_BADGE.live}`}>
                live
              </span>
            ) : (
              <span className={`inline rounded px-1 py-0.5 text-xs font-medium ${SOURCE_BADGE.fallback}`}>
                fallback
              </span>
            )}
          </label>
          {gridSource === "fallback" ? (
            <input
              type="number"
              min={0}
              step={1}
              value={fallbackGrid}
              onChange={(e) => setFallbackGrid(parseFloat(e.target.value) || 0)}
              className="w-24 rounded-md border border-gray-300 px-2 py-1.5 text-sm focus:outline-none"
            />
          ) : (
            <p className="text-sm font-mono text-gray-700">
              {gridIntensity.toFixed(0)} gCO₂/kWh
            </p>
          )}
        </div>
      </div>

      {/* Table */}
      <div className="overflow-x-auto rounded-lg border border-gray-200 shadow-sm">
        <table className="min-w-full divide-y divide-gray-200 text-sm">
          <thead className="bg-gray-50">
            <tr>
              {[
                "Namespace",
                "Model",
                "Hardware",
                "J / token",
                "kWh / token",
                "Cost / token",
                "gCO₂ / token",
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
            {isLoading ? (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-gray-400">
                  <span className="inline-flex items-center gap-2">
                    <span className="h-4 w-4 animate-spin rounded-full border-2 border-gray-300 border-t-blue-500" />
                    Loading…
                  </span>
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-sm text-gray-400">
                  No measurements yet.
                </td>
              </tr>
            ) : (
              rows.map((r, i) => {
                const m = r.metric;
                const jpt = parseValue(r.value[1]);
                const kwhPerToken = deriveKwhPerToken(jpt, pue);
                const costPerToken = kwhPerToken * costPerKwh;
                const co2PerToken = kwhPerToken * gridIntensity; // gCO₂

                return (
                  <tr key={i} className="hover:bg-gray-50">
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-gray-700">
                      {m.namespace}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-medium text-gray-900">
                      {m.model}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-gray-600">
                      {m.hardware}
                    </td>
                    {/* J/token — raw measurement */}
                    <td className="whitespace-nowrap px-4 py-3">
                      <p className="font-mono font-semibold text-gray-900">
                        {jpt.toFixed(4)}
                      </p>
                    </td>
                    {/* kWh/token with derivation formula inline (AC-7) */}
                    <td className="whitespace-nowrap px-4 py-3">
                      <p className="font-mono font-semibold text-gray-900">
                        {kwhPerToken.toExponential(3)}
                      </p>
                      <p className="text-xs text-gray-400">
                        {jpt.toFixed(4)} × {pue.toFixed(2)} ÷ 3,600,000
                      </p>
                    </td>
                    {/* Cost/token with derivation formula inline (AC-7) */}
                    <td className="whitespace-nowrap px-4 py-3">
                      <p className="font-mono font-semibold text-gray-900">
                        ${costPerToken.toExponential(3)}
                      </p>
                      <p className="text-xs text-gray-400">
                        kWh × ${costPerKwh.toFixed(3)}/kWh
                      </p>
                    </td>
                    {/* gCO₂/token with source badge + derivation formula inline (AC-7) */}
                    <td className="whitespace-nowrap px-4 py-3">
                      <p className="font-mono font-semibold text-gray-900">
                        {co2PerToken.toExponential(3)}
                      </p>
                      <p className="text-xs text-gray-400">
                        kWh × {gridIntensity.toFixed(0)} gCO₂/kWh{" "}
                        <span
                          className={`rounded px-1 py-0.5 font-medium ${SOURCE_BADGE[gridSource] ?? SOURCE_BADGE.fallback}`}
                        >
                          {gridSource}
                        </span>
                      </p>
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
        <div className="border-t border-gray-100 bg-gray-50 px-4 py-2 text-xs text-gray-400">
          kWh/token = J/token × PUE ÷ 3,600,000 · gCO₂/token = kWh/token × grid
          intensity · refreshes every 15s
        </div>
      </div>
    </div>
  );
}
