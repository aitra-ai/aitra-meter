"use client";

import { useState, useMemo } from "react";
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
  ResponsiveContainer,
} from "recharts";
import { useMetricsRange } from "@/lib/useMetricsRange";
import { parseValue } from "@/lib/prometheus";

// Power drawn while inference is running (W)
const Q_SERVING = "aitra_gpu_power_watts * on(node) group_left() (aitra_j_per_token > 0)";
// Total GPU power per node (serving + idle — use raw power)
const Q_TOTAL = "aitra_gpu_power_watts";

type WindowOption = { label: string; hours: number; step: string };
const WINDOWS: WindowOption[] = [
  { label: "1h", hours: 1, step: "60s" },
  { label: "6h", hours: 6, step: "5m" },
  { label: "24h", hours: 24, step: "15m" },
];

const SERVING_COLOR = "#2563eb";
const IDLE_COLOR = "#d1d5db";

function formatTs(epochMs: number): string {
  return new Date(epochMs).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  });
}

interface ChartPoint {
  ts: number;
  serving: number;
  idle: number;
}

export function IdleChart() {
  const [windowIdx, setWindowIdx] = useState(2); // default 24h
  const win = WINDOWS[windowIdx];

  const now = Math.floor(Date.now() / 1000);
  const start = now - win.hours * 3600;

  const params = { start, end: now, step: win.step };

  const { data: servingData, error: servingErr, isLoading: servingLoading } =
    useMetricsRange({ query: Q_SERVING, ...params });
  const { data: totalData, error: totalErr, isLoading: totalLoading } =
    useMetricsRange({ query: Q_TOTAL, ...params });

  const error = servingErr || totalErr;
  const isLoading = servingLoading || totalLoading;

  const points = useMemo((): ChartPoint[] => {
    const servingMap = new Map<number, number>();
    for (const s of servingData ?? []) {
      for (const [t, v] of s.values) {
        const val = parseValue(v);
        if (!isNaN(val)) servingMap.set(t * 1000, (servingMap.get(t * 1000) ?? 0) + val);
      }
    }
    const totalMap = new Map<number, number>();
    for (const s of totalData ?? []) {
      for (const [t, v] of s.values) {
        const val = parseValue(v);
        if (!isNaN(val)) totalMap.set(t * 1000, (totalMap.get(t * 1000) ?? 0) + val);
      }
    }

    const allTs = new Set([...servingMap.keys(), ...totalMap.keys()]);
    return Array.from(allTs)
      .sort()
      .map((ts) => {
        const total = totalMap.get(ts) ?? 0;
        const serving = Math.min(servingMap.get(ts) ?? 0, total);
        return { ts, serving, idle: Math.max(total - serving, 0) };
      });
  }, [servingData, totalData]);

  if (error) {
    return (
      <div className="rounded-md border border-red-200 bg-red-50 p-4 text-sm text-red-700">
        <strong>Could not reach Prometheus:</strong> {error.message}
      </div>
    );
  }

  return (
    <div data-testid={!isLoading ? "view-4-ready" : undefined}>
      <div className="mb-3 flex items-center justify-between">
        <div>
          <h3 className="text-sm font-semibold text-gray-700">
            GPU Power — Serving vs Idle (cluster total, W)
          </h3>
          <p className="text-xs text-gray-400">
            Idle = total GPU power with no active inference tokens
          </p>
        </div>
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
      </div>

      {isLoading ? (
        <div className="flex h-48 items-center justify-center text-sm text-gray-400">
          <span className="mr-2 h-4 w-4 animate-spin rounded-full border-2 border-gray-300 border-t-blue-500" />
          Loading…
        </div>
      ) : points.length === 0 ? (
        <p className="text-sm text-gray-400">No GPU power data available.</p>
      ) : (
        <ResponsiveContainer width="100%" height={260}>
          <AreaChart data={points} margin={{ top: 4, right: 16, left: 0, bottom: 0 }}>
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
              label={{ value: "W", angle: -90, position: "insideLeft", style: { fontSize: 11 } }}
            />
            <Tooltip
              formatter={(v, name) => [`${Number(v).toFixed(1)} W`, name as string]}
              labelFormatter={(l) => new Date(Number(l)).toLocaleString()}
            />
            <Legend wrapperStyle={{ fontSize: 11 }} />
            <Area
              type="monotone"
              dataKey="serving"
              stackId="1"
              stroke={SERVING_COLOR}
              fill={SERVING_COLOR}
              fillOpacity={0.7}
              name="Serving"
              isAnimationActive={false}
            />
            <Area
              type="monotone"
              dataKey="idle"
              stackId="1"
              stroke={IDLE_COLOR}
              fill={IDLE_COLOR}
              fillOpacity={0.8}
              name="Idle"
              isAnimationActive={false}
            />
          </AreaChart>
        </ResponsiveContainer>
      )}
    </div>
  );
}
