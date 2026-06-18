package aggregation

// SiteParams carries the per-site conversion factors the aggregation loop uses to
// derive cost and carbon from J/token. They come from the aggregation-service
// configuration (Helm `siteConfig`). A zero ElectricityCostPerKWh or
// GridGCO2PerKWh disables the corresponding derivation, so the cost/carbon
// metrics are simply not set until a site is configured.
type SiteParams struct {
	ElectricityCostPerKWh float64 // USD per kWh
	GridGCO2PerKWh        float64 // grid carbon intensity, gCO2 per kWh
	CarbonSource          string  // carbon_source metric label; defaults to "manual"
	CostSource            string  // cost_source metric label; defaults to "manual"
}

func (s SiteParams) carbonSourceLabel() string {
	if s.CarbonSource == "" {
		return "manual"
	}
	return s.CarbonSource
}

func (s SiteParams) costSourceLabel() string {
	if s.CostSource == "" {
		return "manual"
	}
	return s.CostSource
}
