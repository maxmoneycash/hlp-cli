package exchange

import (
	"context"
)

// OrderWire is one order in an "order" action. Field order is signed.
type OrderWire struct {
	A int       `msgpack:"a" json:"a"`
	B bool      `msgpack:"b" json:"b"`
	P string    `msgpack:"p" json:"p"`
	S string    `msgpack:"s" json:"s"`
	R bool      `msgpack:"r" json:"r"`
	T OrderType `msgpack:"t" json:"t"`
	C string    `msgpack:"c,omitempty" json:"c,omitempty"` // cloid "0x" + 32 hex
}

// OrderType is {"limit":{"tif":...}} or {"trigger":{...}}; exactly one set.
type OrderType struct {
	Limit   *LimitType   `msgpack:"limit,omitempty" json:"limit,omitempty"`
	Trigger *TriggerType `msgpack:"trigger,omitempty" json:"trigger,omitempty"`
}

// LimitType: Tif is Alo | Ioc | Gtc.
type LimitType struct {
	Tif string `msgpack:"tif" json:"tif"`
}

// TriggerType: field order isMarket, triggerPx, tpsl.
type TriggerType struct {
	IsMarket  bool   `msgpack:"isMarket" json:"isMarket"`
	TriggerPx string `msgpack:"triggerPx" json:"triggerPx"`
	Tpsl      string `msgpack:"tpsl" json:"tpsl"` // tp | sl
}

// Builder is the optional builder-fee attachment (opt-in only).
type Builder struct {
	B string `msgpack:"b" json:"b"`
	F int    `msgpack:"f" json:"f"` // tenths of a bp; <= 100 perps
}

// OrderAction: type, orders, grouping, builder?.
type OrderAction struct {
	Type     string      `msgpack:"type" json:"type"` // "order"
	Orders   []OrderWire `msgpack:"orders" json:"orders"`
	Grouping string      `msgpack:"grouping" json:"grouping"` // na | normalTpsl | positionTpsl
	Builder  *Builder    `msgpack:"builder,omitempty" json:"builder,omitempty"`
}

// CancelAction: type, cancels[{a,o}]. Never send "f":false.
type CancelAction struct {
	Type    string   `msgpack:"type" json:"type"` // "cancel"
	Cancels []Cancel `msgpack:"cancels" json:"cancels"`
}

// Cancel is one {a,o}.
type Cancel struct {
	A int    `msgpack:"a" json:"a"`
	O uint64 `msgpack:"o" json:"o"`
}

// UpdateLeverageAction: type, asset, isCross, leverage.
type UpdateLeverageAction struct {
	Type     string `msgpack:"type" json:"type"` // "updateLeverage"
	Asset    int    `msgpack:"asset" json:"asset"`
	IsCross  bool   `msgpack:"isCross" json:"isCross"`
	Leverage int    `msgpack:"leverage" json:"leverage"`
}

// VaultTransferAction: type, vaultAddress, isDeposit, usd (micro-USDC).
// Signed with vault=nil (it is about the signer's own account).
type VaultTransferAction struct {
	Type         string `msgpack:"type" json:"type"` // "vaultTransfer"
	VaultAddress string `msgpack:"vaultAddress" json:"vaultAddress"`
	IsDeposit    bool   `msgpack:"isDeposit" json:"isDeposit"`
	USD          uint64 `msgpack:"usd" json:"usd"`
}

// ScheduleCancelAction is the dead-man switch; Time omitted = unset.
type ScheduleCancelAction struct {
	Type string  `msgpack:"type" json:"type"` // "scheduleCancel"
	Time *uint64 `msgpack:"time,omitempty" json:"time,omitempty"`
}

// Signature is the {r,s,v} object.
type Signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V int    `json:"v"` // 27 | 28
}

// Envelope is the POST /exchange body.
type Envelope struct {
	Action       any       `json:"action"`
	Nonce        uint64    `json:"nonce"`
	Signature    Signature `json:"signature"`
	VaultAddress *string   `json:"vaultAddress"`
	ExpiresAfter *uint64   `json:"expiresAfter,omitempty"`
}

// Signer signs L1 actions with an agent (API wallet) key.
// The key lives only inside the Signer; it is never exposed or logged.
type Signer interface {
	Address() string // lowercase 0x address of the agent key
	// SignL1 computes connectionId = keccak(msgpack(action) || nonce u64be ||
	// vaultFlag[||vault20] || [0x00||expiresAfter u64be]) and signs the
	// EIP-712 Agent{source,connectionId} on domain Exchange/1/1337/0x0.
	SignL1(action any, nonce uint64, vault *string, expiresAfter *uint64, mainnet bool) (Signature, error)
}

// Gate is the last line of defense, set by core per call.
type Gate struct {
	Live    bool // false => Post returns the signed envelope without sending (dry run)
	Mainnet bool
}

// Result of Post: per-item statuses are inside an "ok" envelope.
type Result struct {
	DryRun   bool     `json:"dryRun"`
	Envelope Envelope `json:"envelope"`
	Status   string   `json:"status,omitempty"` // ok | err
	Response any      `json:"response,omitempty"`
	Unknown  bool     `json:"unknownOutcome,omitempty"` // 5xx/timeout after send: do NOT resend
	// Statuses are the per-item results inside an "ok" envelope (orders,
	// cancels); an item can fail even when Status is "ok".
	Statuses []ItemStatus `json:"statuses,omitempty"`
}

// Client posts signed envelopes. Nonces: atomic max(now_ms, last+1).
type Client interface {
	Post(ctx context.Context, action any, vault *string, gate Gate) (Result, error)
}
