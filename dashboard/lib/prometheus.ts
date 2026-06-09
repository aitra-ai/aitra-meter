/**
 * Thin client for the Prometheus HTTP API.
 * All queries go through the Next.js API route (/api/metrics) to avoid
 * CORS issues and to keep Prometheus internal to the cluster.
 */

export interface PrometheusResult {
  metric: Record<string, string>;
  value: [number, string]; // [timestamp, value]
}

export interface PrometheusResponse {
  status: "success" | "error";
  data: {
    resultType: "vector";
    result: PrometheusResult[];
  };
  error?: string;
}

export interface PrometheusRangeSeries {
  metric: Record<string, string>;
  values: [number, string][]; // [timestamp, value]
}

export interface PrometheusRangeResponse {
  status: "success" | "error";
  data: {
    resultType: "matrix";
    result: PrometheusRangeSeries[];
  };
  error?: string;
}

/** Fetch an instant vector query from the Prometheus API. */
export async function queryPrometheus(
  prometheusUrl: string,
  query: string,
): Promise<PrometheusResult[]> {
  const url = new URL("/api/v1/query", prometheusUrl);
  url.searchParams.set("query", query);

  const res = await fetch(url.toString(), {
    next: { revalidate: 0 }, // always fresh
  });
  if (!res.ok) {
    throw new Error(`Prometheus query failed: ${res.status} ${res.statusText}`);
  }
  const json: PrometheusResponse = await res.json();
  if (json.status !== "success") {
    throw new Error(`Prometheus error: ${json.error ?? "unknown"}`);
  }
  return json.data.result;
}

/** Fetch a range (matrix) query from the Prometheus API. */
export async function queryPrometheusRange(
  prometheusUrl: string,
  query: string,
  start: number,
  end: number,
  step: string,
): Promise<PrometheusRangeSeries[]> {
  const url = new URL("/api/v1/query_range", prometheusUrl);
  url.searchParams.set("query", query);
  url.searchParams.set("start", String(start));
  url.searchParams.set("end", String(end));
  url.searchParams.set("step", step);

  const res = await fetch(url.toString(), {
    next: { revalidate: 0 },
  });
  if (!res.ok) {
    throw new Error(`Prometheus range query failed: ${res.status} ${res.statusText}`);
  }
  const json: PrometheusRangeResponse = await res.json();
  if (json.status !== "success") {
    throw new Error(`Prometheus error: ${json.error ?? "unknown"}`);
  }
  return json.data.result;
}

/** Parse a float from a Prometheus value string, returning NaN on failure. */
export function parseValue(v: string): number {
  return parseFloat(v);
}
