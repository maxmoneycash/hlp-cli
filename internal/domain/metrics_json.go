package domain

import "encoding/json"

// Keep the established numeric Go fields and JSON schema while distinguishing
// measured zero from unavailable trade metrics. Nonzero legacy structs remain
// serializable even when their caller predates the internal presence flags.
func (m Metrics) MarshalJSON() ([]byte, error) {
	type wire Metrics
	var winRate, profitFactor *float64
	if m.WinRateKnown || m.WinRate != 0 {
		winRate = &m.WinRate
	}
	if m.ProfitFactorKnown || m.ProfitFactor != 0 {
		profitFactor = &m.ProfitFactor
	}
	return json.Marshal(struct {
		wire
		WinRate      *float64 `json:"winRate,omitempty"`
		ProfitFactor *float64 `json:"profitFactor,omitempty"`
	}{wire(m), winRate, profitFactor})
}

// Persisted results must retain the presence of zero when decoded and later
// printed again. Absent/null fields remain unavailable.
func (m *Metrics) UnmarshalJSON(data []byte) error {
	type wire Metrics
	var raw wire
	value := struct {
		*wire
		WinRate      *float64 `json:"winRate"`
		ProfitFactor *float64 `json:"profitFactor"`
	}{wire: &raw}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*m = Metrics(raw)
	if value.WinRate != nil {
		m.WinRate, m.WinRateKnown = *value.WinRate, true
	}
	if value.ProfitFactor != nil {
		m.ProfitFactor, m.ProfitFactorKnown = *value.ProfitFactor, true
	}
	return nil
}
