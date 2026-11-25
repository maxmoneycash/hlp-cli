package exchange

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/maxmoneycash/hlp/internal/hyperliquid"
)

// Additional ordered action structs (field order is the signed order) and
// builders that apply the wire rules (trimmed decimals, lowercase
// addresses, omitted optionals).

// CancelByCloidAction: type, cancels[{asset, cloid}].
type CancelByCloidAction struct {
	Type    string        `msgpack:"type" json:"type"` // "cancelByCloid"
	Cancels []CancelCloid `msgpack:"cancels" json:"cancels"`
}

// CancelCloid is one {asset, cloid}.
type CancelCloid struct {
	Asset int    `msgpack:"asset" json:"asset"`
	Cloid string `msgpack:"cloid" json:"cloid"`
}

// Modify is one {oid, order} of batchModify. OID is a uint64 oid.
type Modify struct {
	OID   uint64    `msgpack:"oid" json:"oid"`
	Order OrderWire `msgpack:"order" json:"order"`
}

// BatchModifyAction: type, modifies[{oid, order}]. Resend the original tif
// and reduce-only flag or they change.
type BatchModifyAction struct {
	Type     string   `msgpack:"type" json:"type"` // "batchModify"
	Modifies []Modify `msgpack:"modifies" json:"modifies"`
}

// UpdateIsolatedMarginAction: type, asset, isBuy, ntli (signed micro-USD).
type UpdateIsolatedMarginAction struct {
	Type  string `msgpack:"type" json:"type"` // "updateIsolatedMargin"
	Asset int    `msgpack:"asset" json:"asset"`
	IsBuy bool   `msgpack:"isBuy" json:"isBuy"`
	Ntli  int    `msgpack:"ntli" json:"ntli"`
}

// NoopAction burns a nonce (invalidates in-flight orders with lower nonces).
type NoopAction struct {
	Type string `msgpack:"type" json:"type"` // "noop"
}

// Time-in-force values.
const (
	TifGtc = "Gtc"
	TifIoc = "Ioc"
	TifAlo = "Alo"
)

// NewCloid returns a random client order id: "0x" + 32 lowercase hex.
func NewCloid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "0x" + hex.EncodeToString(b[:])
}

// ValidCloid reports whether s is "0x" + 32 hex chars.
func ValidCloid(s string) bool {
	if len(s) != 34 || !strings.HasPrefix(s, "0x") {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}

// LimitOrder builds an order wire with canonical decimal strings. The price
// must already be on the grid (use hyperliquid.ValidPx / RoundPx first).
func LimitOrder(asset int, isBuy bool, px, sz decimal.Decimal, tif string, reduceOnly bool, cloid string) (OrderWire, error) {
	switch tif {
	case TifGtc, TifIoc, TifAlo:
	default:
		return OrderWire{}, fmt.Errorf("exchange: tif must be Gtc, Ioc or Alo, got %q", tif)
	}
	if px.Sign() <= 0 || sz.Sign() <= 0 {
		return OrderWire{}, fmt.Errorf("exchange: price and size must be positive")
	}
	if cloid != "" {
		cloid = strings.ToLower(cloid)
		if !ValidCloid(cloid) {
			return OrderWire{}, fmt.Errorf("exchange: cloid must be 0x + 32 hex chars")
		}
	}
	return OrderWire{
		A: asset, B: isBuy, P: hyperliquid.ToWire(px), S: hyperliquid.ToWire(sz), R: reduceOnly,
		T: OrderType{Limit: &LimitType{Tif: tif}}, C: cloid,
	}, nil
}

// TriggerOrder builds a TP/SL trigger order (reduce-only by convention).
func TriggerOrder(asset int, isBuy bool, px, sz, triggerPx decimal.Decimal, isMarket bool, tpsl string, reduceOnly bool, cloid string) (OrderWire, error) {
	if tpsl != "tp" && tpsl != "sl" {
		return OrderWire{}, fmt.Errorf("exchange: tpsl must be tp or sl")
	}
	w, err := LimitOrder(asset, isBuy, px, sz, TifGtc, reduceOnly, cloid)
	if err != nil {
		return OrderWire{}, err
	}
	w.T = OrderType{Trigger: &TriggerType{IsMarket: isMarket, TriggerPx: hyperliquid.ToWire(triggerPx), Tpsl: tpsl}}
	return w, nil
}

// NewOrderAction wraps orders with grouping "na" (no builder fee).
func NewOrderAction(orders ...OrderWire) OrderAction {
	return OrderAction{Type: "order", Orders: orders, Grouping: "na"}
}

// NewCancelAction cancels oids on one asset each.
func NewCancelAction(cancels ...Cancel) CancelAction {
	return CancelAction{Type: "cancel", Cancels: cancels}
}

// NewUpdateLeverage builds updateLeverage.
func NewUpdateLeverage(asset int, cross bool, leverage int) UpdateLeverageAction {
	return UpdateLeverageAction{Type: "updateLeverage", Asset: asset, IsCross: cross, Leverage: leverage}
}

// NewVaultTransfer builds a deposit (true) or withdrawal of usd dollars
// (converted to micro-USDC, floored). Signed with vault = nil.
func NewVaultTransfer(vault string, deposit bool, usd decimal.Decimal) (VaultTransferAction, error) {
	a, err := parseAddress(vault)
	if err != nil {
		return VaultTransferAction{}, err
	}
	if usd.Sign() <= 0 {
		return VaultTransferAction{}, fmt.Errorf("exchange: vault transfer amount must be positive")
	}
	micro := usd.Shift(6).Floor()
	return VaultTransferAction{Type: "vaultTransfer", VaultAddress: fmt.Sprintf("0x%x", a), IsDeposit: deposit, USD: micro.BigInt().Uint64()}, nil
}

// NewScheduleCancel sets (t > 0, ms) or clears (t == 0) the dead-man switch.
func NewScheduleCancel(t uint64) ScheduleCancelAction {
	a := ScheduleCancelAction{Type: "scheduleCancel"}
	if t > 0 {
		a.Time = &t
	}
	return a
}
