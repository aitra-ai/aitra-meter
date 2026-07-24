/**
 * Server-side proxy for Prometheus queries.
 * Keeps the Prometheus URL internal and avoids browser CORS restrictions.
 * Queries are restricted to aitra_* metrics (see lib/promguard) so a public
 * deployment cannot be used as an open PromQL gateway.
 *
 * GET /api/metrics?query=<promql>
 */

import { NextRequest, NextResponse } from "next/server";
import { queryPrometheus, PrometheusResult } from "@/lib/prometheus";
import { assertAllowedQuery } from "@/lib/promguard";
import { upstreamHeaders } from "@/lib/upstream";

const PROMETHEUS_URL =
  process.env.PROMETHEUS_URL ?? "http://localhost:9090";

export async function GET(req: NextRequest): Promise<NextResponse> {
  const query = req.nextUrl.searchParams.get("query");
  if (!query) {
    return NextResponse.json({ error: "query parameter required" }, { status: 400 });
  }

  try {
    assertAllowedQuery(query);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return NextResponse.json({ error: message }, { status: 403 });
  }

  try {
    const results: PrometheusResult[] = await queryPrometheus(
      PROMETHEUS_URL,
      query,
      upstreamHeaders(),
    );
    return NextResponse.json({ results });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return NextResponse.json({ error: message }, { status: 502 });
  }
}
