"use client";

import useSWR from "swr";
import type { PrometheusRangeSeries } from "./prometheus";

const REFRESH_INTERVAL_MS = 60_000; // range charts refresh every 60s

interface RangeParams {
  query: string;
  start: number;
  end: number;
  step: string;
}

async function fetchRange(params: RangeParams): Promise<PrometheusRangeSeries[]> {
  const { query, start, end, step } = params;
  const qs = new URLSearchParams({
    query,
    start: String(start),
    end: String(end),
    step,
  });
  const res = await fetch(`/api/metrics/range?${qs}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const json = await res.json();
  if (json.error) throw new Error(json.error);
  return json.results as PrometheusRangeSeries[];
}

export function useMetricsRange(params: RangeParams) {
  return useSWR<PrometheusRangeSeries[]>(
    // Key includes all params so changing window triggers a new fetch.
    ["range", params.query, params.start, params.end, params.step],
    () => fetchRange(params),
    {
      refreshInterval: REFRESH_INTERVAL_MS,
      revalidateOnFocus: false,
    },
  );
}
