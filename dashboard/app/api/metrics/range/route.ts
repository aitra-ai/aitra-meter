/**
 * Server-side proxy for Prometheus range queries.
 *
 * GET /api/metrics/range?query=<promql>&start=<unix>&end=<unix>&step=<duration>
 */

import { NextRequest, NextResponse } from "next/server";
import { queryPrometheusRange, PrometheusRangeSeries } from "@/lib/prometheus";

const PROMETHEUS_URL =
  process.env.PROMETHEUS_URL ?? "http://localhost:9090";

export async function GET(req: NextRequest): Promise<NextResponse> {
  const { searchParams } = req.nextUrl;
  const query = searchParams.get("query");
  const start = searchParams.get("start");
  const end = searchParams.get("end");
  const step = searchParams.get("step") ?? "60s";

  if (!query || !start || !end) {
    return NextResponse.json(
      { error: "query, start, and end parameters are required" },
      { status: 400 },
    );
  }

  try {
    const results: PrometheusRangeSeries[] = await queryPrometheusRange(
      PROMETHEUS_URL,
      query,
      parseFloat(start),
      parseFloat(end),
      step,
    );
    return NextResponse.json({ results });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return NextResponse.json({ error: message }, { status: 502 });
  }
}
