import { JPerTokenTable } from "@/components/JPerTokenTable";
import { TrendChart } from "@/components/TrendChart";
import { ChargebackTable } from "@/components/ChargebackTable";
import { IdleChart } from "@/components/IdleChart";
import { CarbonCostTable } from "@/components/CarbonCostTable";

function Section({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <section>
      <div className="mb-3">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-gray-500">
          {title}
        </h2>
        {description && (
          <p className="mt-0.5 text-xs text-gray-400">{description}</p>
        )}
      </div>
      {children}
    </section>
  );
}

export default function HomePage() {
  return (
    <main className="mx-auto max-w-7xl px-4 py-8 sm:px-6 lg:px-8">
      <div className="mb-8">
        <h1 className="text-2xl font-bold text-gray-900">Aitra Meter</h1>
        <p className="mt-1 text-sm text-gray-500">
          AI inference energy measurement · live + historical
        </p>
      </div>

      <div className="space-y-12">
        {/* View 1 */}
        <Section
          title="J / Token — Live"
          description="Latest measurement window per workload × model × hardware"
        >
          <JPerTokenTable />
        </Section>

        {/* Views 2a + 2b */}
        <Section
          title="J / Token — Trend"
          description="Range query over Prometheus; cluster aggregate and per-series breakdown"
        >
          <TrendChart />
        </Section>

        {/* View 3 */}
        <Section
          title="Namespace Chargeback"
          description="30-day energy aggregates · PUE and cost derived client-side"
        >
          <ChargebackTable />
        </Section>

        {/* View 4 */}
        <Section
          title="Idle Consumption"
          description="GPU power split between active inference and idle — stacked area per cluster"
        >
          <IdleChart />
        </Section>

        {/* View 5 */}
        <Section
          title="Carbon & Cost per Token"
          description="Derivation formula shown inline for every figure (AC-7) · grid intensity source badge"
        >
          <CarbonCostTable />
        </Section>
      </div>
    </main>
  );
}
