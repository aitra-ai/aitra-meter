"use client";

import { useState, useMemo } from "react";
import useSWR from "swr";

interface ChargebackRow {
  namespace: string;
  workload: string;
  model: string;
  hardware: string;
  calibration_tier: string;
  attribution_method: string;
  energy_joules: number;
  token_count: number;
}

const PERIOD_OPTIONS = [7, 14, 30, 90] as const;
type Period = (typeof PERIOD_OPTIONS)[number];

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

async function fetchChargeback(days: number): Promise<ChargebackRow[]> {
  const res = await fetch(`/api/chargeback?days=${days}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const json = await res.json();
  if (json.error) throw new Error(json.error);
  return json.rows as ChargebackRow[];
}

/** Convert joules → kWh, apply PUE, multiply by cost rate. */
function deriveKwh(joules: number, pue: number): number {
  return (joules * pue) / 3_600_000;
}

function deriveCost(joules: number, pue: number, costPerKwh: number): number {
  return deriveKwh(joules, pue) * costPerKwh;
}

function exportCsv(
  rows: ChargebackRow[],
  pue: number,
  costPerKwh: number,
  days: number,
) {
  const header =
    "Namespace,Workload,Model,Hardware,Tier,Attribution,Energy (kWh),Cost (USD),Tokens\n";
  const body = rows
    .map((r) => {
      const kwh = deriveKwh(r.energy_joules, pue).toFixed(4);
      const cost = deriveCost(r.energy_joules, pue, costPerKwh).toFixed(4);
      return [
        r.namespace,
        r.workload,
        r.model,
        r.hardware,
        r.calibration_tier,
        r.attribution_method,
        kwh,
        cost,
        r.token_count,
      ]
        .map((v) => `"${String(v).replace(/"/g, '""')}"`)
        .join(",");
    })
    .join("\n");

  const blob = new Blob([header + body], { type: "text/csv" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `aitra-chargeback-${days}d.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

export function ChargebackTable() {
  const [days, setDays] = useState<Period>(30);
  const [pue, setPue] = useState(1.2);
  const [costPerKwh, setCostPerKwh] = useState(0.08);

  const { data, error, isLoading } = useSWR(
    ["chargeback", days],
    ([, d]) => fetchChargeback(d),
    { revalidateOnFocus: false },
  );

  const rows = useMemo(() => data ?? [], [data]);

  const totals = useMemo(() => {
    const kwh = rows.reduce((s, r) => s + deriveKwh(r.energy_joules, pue), 0);
    const cost = rows.reduce((s, r) => s + deriveCost(r.energy_joules, pue, costPerKwh), 0);
    const tokens = rows.reduce((s, r) => s + r.token_count, 0);
    return { kwh, cost, tokens };
  }, [rows, pue, costPerKwh]);

  if (error) {
    return (
      <div className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700">
        <strong>Could not load chargeback data:</strong> {error.message}
        <p className="mt-1 text-xs text-red-500">
          Check that the aggregation service is reachable and the storage backend is configured.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      {/* Controls */}
      <div className="flex flex-wrap items-end gap-6">
        {/* Period selector */}
        <div>
          <label className="mb-1 block text-xs font-medium text-gray-500">
            Period
          </label>
          <div className="flex rounded-md border border-gray-300 bg-white text-sm">
            {PERIOD_OPTIONS.map((d) => (
              <button
                key={d}
                onClick={() => setDays(d)}
                className={`px-3 py-1.5 first:rounded-l-md last:rounded-r-md focus:outline-none ${
                  days === d
                    ? "bg-blue-600 text-white"
                    : "text-gray-700 hover:bg-gray-50"
                }`}
              >
                {d}d
              </button>
            ))}
          </div>
        </div>

        {/* PUE slider */}
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

        {/* Cost per kWh */}
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

        <button
          onClick={() => rows.length && exportCsv(rows, pue, costPerKwh, days)}
          disabled={rows.length === 0}
          className="ml-auto rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-700 hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-40"
        >
          Export CSV
        </button>
      </div>

      {/* Table */}
      <div data-testid={!isLoading ? "view-3-ready" : undefined} className="overflow-x-auto rounded-lg border border-gray-200 shadow-sm">
        <table className="min-w-full divide-y divide-gray-200 text-sm">
          <thead className="bg-gray-50">
            <tr>
              {[
                "Namespace",
                "Workload",
                "Model",
                "Hardware",
                "Tier",
                "Attribution",
                "Energy (kWh)",
                "Cost (USD)",
                "Tokens",
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
                <td colSpan={9} className="px-4 py-8 text-center text-gray-400">
                  <span className="inline-flex items-center gap-2">
                    <span className="h-4 w-4 animate-spin rounded-full border-2 border-gray-300 border-t-blue-500" />
                    Loading…
                  </span>
                </td>
              </tr>
            ) : rows.length === 0 ? (
              <tr>
                <td
                  colSpan={9}
                  className="px-4 py-8 text-center text-sm text-gray-500"
                >
                  No data for the selected period.
                </td>
              </tr>
            ) : (
              rows.map((r, i) => {
                const kwh = deriveKwh(r.energy_joules, pue);
                const cost = deriveCost(r.energy_joules, pue, costPerKwh);
                return (
                  <tr key={i} className="hover:bg-gray-50">
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-gray-700">
                      {r.namespace}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3">
                      {!r.workload || r.workload === "unknown" ? (
                        <span className="italic text-gray-400">unknown</span>
                      ) : (
                        r.workload
                      )}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-medium text-gray-900">
                      {r.model}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-gray-600">
                      {r.hardware}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3">
                      <TierBadge tier={r.calibration_tier} />
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-gray-600">
                      {r.attribution_method || "—"}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-gray-900">
                      {kwh.toFixed(4)}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-gray-900">
                      ${cost.toFixed(4)}
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-gray-600">
                      {r.token_count.toLocaleString()}
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
          {!isLoading && rows.length > 0 && (
            <tfoot className="bg-gray-50">
              <tr className="border-t border-gray-200 font-semibold">
                <td colSpan={6} className="px-4 py-2 text-xs text-gray-500">
                  Total ({rows.length} combinations)
                </td>
                <td className="whitespace-nowrap px-4 py-2 font-mono text-sm">
                  {totals.kwh.toFixed(4)}
                </td>
                <td className="whitespace-nowrap px-4 py-2 font-mono text-sm">
                  ${totals.cost.toFixed(4)}
                </td>
                <td className="whitespace-nowrap px-4 py-2 font-mono text-sm">
                  {totals.tokens.toLocaleString()}
                </td>
              </tr>
            </tfoot>
          )}
        </table>
        <div className="border-t border-gray-100 bg-gray-50 px-4 py-2 text-xs text-gray-400">
          Energy (kWh) = raw joules × PUE ÷ 3,600,000 · Cost = kWh × $
          {costPerKwh.toFixed(3)}/kWh · last {days} days
        </div>
      </div>
    </div>
  );
}
