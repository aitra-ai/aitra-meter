/**
 * Optional HTTP Basic Auth for public deployments.
 *
 * Set DASHBOARD_PASSWORD (and optionally DASHBOARD_USER, default "aitra") to
 * require credentials on every request — pages and API routes alike. Leave it
 * unset for cluster-internal deployments and the middleware is a no-op.
 */

import { NextRequest, NextResponse } from "next/server";

export function middleware(req: NextRequest): NextResponse {
  const password = process.env.DASHBOARD_PASSWORD;
  if (!password) {
    return NextResponse.next();
  }
  const user = process.env.DASHBOARD_USER ?? "aitra";

  const auth = req.headers.get("authorization") ?? "";
  const [scheme, encoded] = auth.split(" ");
  if (scheme === "Basic" && encoded) {
    try {
      const [gotUser, ...rest] = atob(encoded).split(":");
      const gotPass = rest.join(":");
      if (gotUser === user && gotPass === password) {
        return NextResponse.next();
      }
    } catch {
      // fall through to 401
    }
  }

  return new NextResponse("Authentication required", {
    status: 401,
    headers: { "WWW-Authenticate": 'Basic realm="aitra-meter"' },
  });
}
