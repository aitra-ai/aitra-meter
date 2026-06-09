/**
 * Server-side proxy for Prometheus queries.
 * Keeps the Prometheus URL internal and avoids browser CORS restrictions.
 *
 * GET /api/metrics?query=<promql>
 */

import { NextRequest, NextResponse } from "next/server";
import { queryPrometheus, PrometheusResult } from "@/lib/prometheus";

const PROMETHEUS_URL =
  process.env.PROMETHEUS_URL ?? "http://localhost:9090";

export async function GET(req: NextRequest): Promise<NextResponse> {
  const query = req.nextUrl.searchParams.get("query");
  if (!query) {
    return NextResponse.json({ error: "query parameter required" }, { status: 400 });
  }

  try {
    const results: PrometheusResult[] = await queryPrometheus(PROMETHEUS_URL, query);
    return NextResponse.json({ results });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return NextResponse.json({ error: message }, { status: 502 });
  }
}
