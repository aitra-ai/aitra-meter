/**
 * PromQL allowlist guard for the /api/metrics proxy routes.
 *
 * When the dashboard is deployed on a public host (e.g. Vercel) the proxy
 * would otherwise forward arbitrary PromQL to the internal Prometheus. This
 * guard restricts queries to aitra_* metrics plus a fixed set of PromQL
 * functions, keywords and label names — enough for every query the dashboard
 * issues, nothing more.
 */

const MAX_QUERY_LENGTH = 1000;

/** PromQL functions / operators / keywords the dashboard's queries may use. */
const ALLOWED_KEYWORDS = new Set([
  // aggregation & grouping
  "sum", "avg", "min", "max", "count", "by", "without",
  // vector matching
  "on", "ignoring", "group_left", "group_right",
  // binary/set operators & modifiers
  "and", "or", "unless", "bool", "offset",
  // counter/gauge functions
  "rate", "irate", "increase", "delta", "idelta",
  "avg_over_time", "sum_over_time", "min_over_time", "max_over_time", "last_over_time",
  "clamp_min", "clamp_max", "abs", "round",
  // range durations parse as identifiers when bare (e.g. 5m in [5m])
  "s", "m", "h", "d", "w", "y",
]);

/** Label names that may appear in matchers and grouping clauses. */
const ALLOWED_LABELS = new Set([
  "node", "model_name", "namespace", "workload", "model", "hardware",
  "precision", "cluster", "instance", "job", "calibration_tier",
  "attribution_method", "carbon_source", "cost_source", "source", "le",
]);

/**
 * Throws if the query references anything outside the allowlist. Every bare
 * identifier must be an aitra_* metric, an allowed PromQL keyword/function,
 * or an allowed label name.
 */
export function assertAllowedQuery(query: string): void {
  if (query.length > MAX_QUERY_LENGTH) {
    throw new Error("query too long");
  }
  // Label values live in string literals; drop them before scanning.
  const stripped = query.replace(/"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'/g, '""');
  const identifiers = stripped.match(/[a-zA-Z_:][a-zA-Z0-9_:]*/g) ?? [];
  for (const id of identifiers) {
    if (id.startsWith("aitra_")) continue;
    if (ALLOWED_KEYWORDS.has(id)) continue;
    if (ALLOWED_LABELS.has(id)) continue;
    throw new Error(`query rejected: identifier "${id}" is not allowed`);
  }
}
