/**
 * Server-side proxy for the aggregation service chargeback endpoint.
 * Keeps the aggregation service address internal and avoids browser CORS.
 *
 * GET /api/chargeback?from=<RFC3339>&to=<RFC3339>&pue=<float>&days=<int>
 *
 * "days" is a convenience shorthand: sets from = now-Nd, to = now.
 * Returns the aggregation service response unchanged.
 */

import { NextRequest, NextResponse } from "next/server";

const AGGREGATION_URL =
  process.env.AGGREGATION_URL ?? "http://aitra-meter-aggregation:8080";

export async function GET(req: NextRequest): Promise<NextResponse> {
  const { searchParams } = req.nextUrl;

  // Build upstream query string — pass through from/to/pue if set,
  // otherwise derive from the "days" convenience param.
  const params = new URLSearchParams();

  const daysParam = searchParams.get("days");
  const fromParam = searchParams.get("from");
  const toParam = searchParams.get("to");
  const pueParam = searchParams.get("pue");

  if (fromParam && toParam) {
    params.set("from", fromParam);
    params.set("to", toParam);
  } else {
    const days = daysParam ? parseInt(daysParam, 10) : 30;
    const to = new Date();
    const from = new Date(to.getTime() - days * 86_400_000);
    params.set("from", from.toISOString());
    params.set("to", to.toISOString());
  }

  if (pueParam) params.set("pue", pueParam);

  const upstream = `${AGGREGATION_URL}/api/v1/namespaces?${params.toString()}`;

  try {
    const res = await fetch(upstream, {
      next: { revalidate: 0 },
    });
    if (!res.ok) {
      const body = await res.text();
      return NextResponse.json(
        { error: `aggregation service error (${res.status}): ${body}` },
        { status: 502 },
      );
    }
    const data = await res.json();
    // Normalise: aggregation service returns { namespaces: [...] }
    // Dashboard expects { rows: [...] }
    const rows = (data.namespaces ?? []).map(
      (n: {
        Namespace: string;
        EnergyJoulesRaw: number;
        EnergyJoulesPUE: number;
        OutputTokens: number;
        AttributionMethod: string;
        Team: string;
        CostCentre: string;
      }) => ({
        namespace: n.Namespace,
        workload: "",
        model: "",
        hardware: "",
        calibration_tier: "",
        attribution_method: n.AttributionMethod,
        energy_joules: n.EnergyJoulesRaw,
        token_count: n.OutputTokens,
      }),
    );
    return NextResponse.json({ rows });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return NextResponse.json({ error: message }, { status: 502 });
  }
}
