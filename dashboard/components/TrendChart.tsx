"use client";

import { useState, useMemo } from "react";
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
import { parseValue } from "@/lib/prometheus";

// View 2a: cluster aggregate — sum of J/token across all series
const Q_JPT_SUM = "avg(aitra_j_per_token)";

// View 2b: one line per workload × model × hardware
const Q_JPT_BY_SERIES = "aitra_j_per_token";

type WindowOption = { label: string; hours: number; step: string };
const WINDOWS: WindowOption[] = [
  { label: "1h", hours: 1, step: "60s" },
  { label: "6h", hours: 6, step: "5m" },
  { label: "24h", hours: 24, step: "15m" },
  { label: "7d", hours: 168, step: "1h" },
];

// Stable color palette for up to 12 series
const PALETTE = [
  "#2563eb", "#16a34a", "#dc2626", "#d97706", "#7c3aed",
  "#0891b2", "#be185d", "#65a30d", "#ea580c", "#0f766e",
  "#9333ea", "#b45309",
];

function seriesLabel(metric: Record<string, string>): string {
  const parts = [metric.workload, metric.model, metric.hardware].filter(Boolean);
  return parts.join(" · ") || metric.__name__ || "cluster";
}

function formatTs(epochMs: number): string {
  return new Date(epochMs).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  });
}

interface ChartData {
  ts: number;
  [key: string]: number;
}

export function TrendChart() {
  const [windowIdx, setWindowIdx] = useState(2); // default 24h
  const win = WINDOWS[windowIdx];

  const now = Math.floor(Date.now() / 1000);
  const start = now - win.hours * 3600;

  // View 2a
  const { data: aggData, error: aggErr, isLoading: aggLoading } = useMetricsRange({
    query: Q_JPT_SUM,
    start,
    end: now,
    step: win.step,
  });

  // View 2b
  const { data: seriesData, error: seriesErr, isLoading: seriesLoading } = useMetricsRange({
    query: Q_JPT_BY_SERIES,
    start,
    end: now,
    step: win.step,
  });

  const error = aggErr || seriesErr;
  const isLoading = aggLoading || seriesLoading;

  // Merge all series into a flat time-keyed map for recharts
  const { aggPoints, seriesPoints, seriesLabels } = useMemo(() => {
    // Aggregate chart
    const aggMap = new Map<number, number>();
    for (const s of aggData ?? []) {
      for (const [t, v] of s.values) {
        const ms = t * 1000;
        const val = parseValue(v);
        if (!isNaN(val)) aggMap.set(ms, val);
      }
    }
    const aggPoints: ChartData[] = Array.from(aggMap.entries())
      .sort(([a], [b]) => a - b)
      .map(([ts, v]) => ({ ts, cluster: v }));

    // Per-series chart
    const labels: string[] = [];
    const tsMap = new Map<number, ChartData>();
    for (const s of seriesData ?? []) {
      const label = seriesLabel(s.metric);
      if (!labels.includes(label)) labels.push(label);
      for (const [t, v] of s.values) {
        const ms = t * 1000;
        const val = parseValue(v);
        if (!isNaN(val)) {
          const row = tsMap.get(ms) ?? { ts: ms };
          (row as Record<string, number>)[label] = val;
          tsMap.set(ms, row);
        }
      }
    }
    const seriesPoints: ChartData[] = Array.from(tsMap.values()).sort(
      (a, b) => a.ts - b.ts,
    );

    return { aggPoints, seriesPoints, seriesLabels: labels };
  }, [aggData, seriesData]);

  if (error) {
    return (
      <div className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700">
        <strong>Could not reach Prometheus:</strong> {error.message}
      </div>
    );
  }

  const WindowToggle = (
    <div className="flex rounded-md border border-gray-300 bg-white text-sm">
      {WINDOWS.map((w, i) => (
        <button
          key={w.label}
          onClick={() => setWindowIdx(i)}
          className={`px-3 py-1.5 first:rounded-l-md last:rounded-r-md focus:outline-none ${
            windowIdx === i ? "bg-blue-600 text-white" : "text-gray-700 hover:bg-gray-50"
          }`}
        >
          {w.label}
        </button>
      ))}
    </div>
  );

  const LoadingOverlay = isLoading ? (
    <div className="flex h-48 items-center justify-center text-sm text-gray-400">
      <span className="mr-2 h-4 w-4 animate-spin rounded-full border-2 border-gray-300 border-t-blue-500" />
      Loading…
    </div>
  ) : null;

  return (
    <div data-testid="view-2-ready" className="space-y-8">
      {/* View 2a — Cluster aggregate */}
      <div>
        <div className="mb-3 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">
            Cluster Average J / Token
          </h3>
          {WindowToggle}
        </div>
        {LoadingOverlay ?? (
          <ResponsiveContainer width="100%" height={240}>
            <LineChart data={aggPoints} margin={{ top: 4, right: 16, left: 0, bottom: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis
                dataKey="ts"
                type="number"
                domain={["dataMin", "dataMax"]}
                tickFormatter={formatTs}
                tick={{ fontSize: 11 }}
                scale="time"
              />
              <YAxis
                tick={{ fontSize: 11 }}
                tickFormatter={(v: number) => v.toFixed(2)}
                label={{ value: "J/tok", angle: -90, position: "insideLeft", style: { fontSize: 11 } }}
              />
              <Tooltip
                formatter={(v) => [Number(v).toFixed(4), "J/token"]}
                labelFormatter={(l) => new Date(Number(l)).toLocaleString()}
              />
              <Line
                dataKey="cluster"
                stroke={PALETTE[0]}
                dot={false}
                strokeWidth={2}
                isAnimationActive={false}
              />
            </LineChart>
          </ResponsiveContainer>
        )}
      </div>

      {/* View 2b — Per-series */}
      <div>
        <h3 className="mb-3 text-sm font-semibold text-gray-700">
          J / Token by Workload × Model × Hardware
        </h3>
        {LoadingOverlay ?? (
          seriesLabels.length === 0 ? (
            <p className="text-sm text-gray-400">No series data yet.</p>
          ) : (
            <ResponsiveContainer width="100%" height={280}>
              <LineChart data={seriesPoints} margin={{ top: 4, right: 16, left: 0, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
                <XAxis
                  dataKey="ts"
                  type="number"
                  domain={["dataMin", "dataMax"]}
                  tickFormatter={formatTs}
                  tick={{ fontSize: 11 }}
                  scale="time"
                />
                <YAxis
                  tick={{ fontSize: 11 }}
                  tickFormatter={(v: number) => v.toFixed(2)}
                  label={{ value: "J/tok", angle: -90, position: "insideLeft", style: { fontSize: 11 } }}
                />
                <Tooltip
                  formatter={(v, name) => [Number(v).toFixed(4), name as string]}
                  labelFormatter={(l) => new Date(Number(l)).toLocaleString()}
                />
                <Legend wrapperStyle={{ fontSize: 11 }} />
                {seriesLabels.map((label, i) => (
                  <Line
                    key={label}
                    dataKey={label}
                    stroke={PALETTE[i % PALETTE.length]}
                    dot={false}
                    strokeWidth={1.5}
                    isAnimationActive={false}
                  />
                ))}
              </LineChart>
            </ResponsiveContainer>
          )
        )}
      </div>
    </div>
  );
}
