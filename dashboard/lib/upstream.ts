/**
 * Auth headers for upstream (tunnel) requests, server-side only.
 *
 * When Prometheus / the aggregation service are reached through an
 * authenticated tunnel instead of the cluster network, the credentials come
 * from env vars so they never leave the server:
 *
 *   PROMETHEUS_BEARER_TOKEN  -> Authorization: Bearer <token>
 *   CF_ACCESS_CLIENT_ID / CF_ACCESS_CLIENT_SECRET
 *                            -> Cloudflare Access service-token headers
 */
export function upstreamHeaders(): Record<string, string> {
  const headers: Record<string, string> = {};
  const bearer = process.env.PROMETHEUS_BEARER_TOKEN;
  if (bearer) {
    headers["Authorization"] = `Bearer ${bearer}`;
  }
  const cfId = process.env.CF_ACCESS_CLIENT_ID;
  const cfSecret = process.env.CF_ACCESS_CLIENT_SECRET;
  if (cfId && cfSecret) {
    headers["CF-Access-Client-Id"] = cfId;
    headers["CF-Access-Client-Secret"] = cfSecret;
  }
  return headers;
}
