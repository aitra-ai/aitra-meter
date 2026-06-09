"use client";

import useSWR from "swr";
import type { PrometheusResult } from "./prometheus";

const REFRESH_INTERVAL_MS = 15_000; // match Prometheus scrape interval

async function fetchMetrics(query: string): Promise<PrometheusResult[]> {
  const res = await fetch(
    `/api/metrics?query=${encodeURIComponent(query)}`,
    { cache: "no-store" },
  );
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const json = await res.json();
  if (json.error) throw new Error(json.error);
  return json.results as PrometheusResult[];
}

export function useMetrics(query: string) {
  return useSWR<PrometheusResult[]>(query, fetchMetrics, {
    refreshInterval: REFRESH_INTERVAL_MS,
    revalidateOnFocus: true,
  });
}
