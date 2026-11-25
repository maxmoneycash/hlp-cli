package hlpxyz

import "encoding/json"

// A real zero balance/NAV is different from an omitted or null wire field.
// Keep existing scalar Go/JSON fields while retaining presence internally.
func (v *LiveVault) UnmarshalJSON(data []byte) error {
	type wire LiveVault
	var decoded wire
	value := struct {
		*wire
		TotalEquity *float64 `json:"totalEquity"`
	}{wire: &decoded}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*v = LiveVault(decoded)
	if value.TotalEquity != nil {
		v.TotalEquity = *value.TotalEquity
		v.totalEquityPresent = true
	}
	return nil
}

func (v *VaultLive) UnmarshalJSON(data []byte) error {
	type wire VaultLive
	var decoded wire
	value := struct {
		*wire
		TotalEquity *float64 `json:"totalEquity"`
	}{wire: &decoded}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*v = VaultLive(decoded)
	if value.TotalEquity != nil {
		v.TotalEquity = *value.TotalEquity
		v.totalEquityPresent = true
	}
	return nil
}

func (n *NAVCurve) UnmarshalJSON(data []byte) error {
	type wire NAVCurve
	var decoded wire
	value := struct {
		*wire
		NavNow *float64 `json:"navNow"`
	}{wire: &decoded}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*n = NAVCurve(decoded)
	if value.NavNow != nil {
		n.NavNow = *value.NavNow
		n.navNowPresent = true
	}
	return nil
}

// Nonzero programmatic structs predate the presence marker and remain valid.
func (v LiveVault) hasTotalEquity() bool { return v.totalEquityPresent || v.TotalEquity != 0 }
func (v VaultLive) hasTotalEquity() bool { return v.totalEquityPresent || v.TotalEquity != 0 }
func (n NAVCurve) hasNavNow() bool       { return n.navNowPresent || n.NavNow != 0 }
