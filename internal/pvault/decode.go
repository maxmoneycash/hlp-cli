package pvault

import (
	"errors"
	"fmt"

	"github.com/maxmoneycash/hlp-cli/internal/evm"
)

// Struct sizes in words. Any other length is rejected: a future struct
// change must fail loudly, never be misread.
const (
	vaultStateWords      = 28
	userStateWords       = 6
	epochDataWords       = 10
	pendingTransferWords = 5
)

// wordReader collects the first decode error so struct decoders stay flat.
type wordReader struct {
	w   evm.Words
	err error
}

func (r *wordReader) fail(i int, err error) {
	if r.err == nil && err != nil {
		r.err = fmt.Errorf("word %d: %w", i, err)
	}
}

func (r *wordReader) big(i int) evm.Big {
	v, err := r.w.Uint(i)
	r.fail(i, err)
	return evm.NewBig(v)
}

func (r *wordReader) int(i int) evm.Big {
	v, err := r.w.Int(i)
	r.fail(i, err)
	return evm.NewBig(v)
}

func (r *wordReader) u64(i int) uint64 {
	v, err := r.w.Uint64(i)
	r.fail(i, err)
	return v
}

func (r *wordReader) u8(i int) uint8 {
	v, err := r.w.Uint8(i)
	r.fail(i, err)
	return v
}

func (r *wordReader) bool(i int) bool {
	v, err := r.w.Bool(i)
	r.fail(i, err)
	return v
}

func (r *wordReader) addr(i int) evm.Address {
	v, err := r.w.Address(i)
	r.fail(i, err)
	return v
}

// DecodeVaultState decodes VaultLens.vaultState: exactly 28 words returned
// inline (no offset word).
func DecodeVaultState(b []byte) (*VaultState, error) {
	w, err := evm.ExpectWords(b, vaultStateWords)
	if err != nil {
		return nil, fmt.Errorf("vaultState: %w", err)
	}
	r := &wordReader{w: w}
	s := &VaultState{
		Epoch: r.u64(0), Phase: Phase(r.u8(1)), CutoffAt: r.u64(2), SettleDeadline: r.u64(3),
		TotalAssets: r.big(4), TotalSupply: r.big(5), SharePrice: r.big(6), HighWaterMark: r.big(7),
		TotalPendingDeposits: r.big(8), TotalPendingRedeemShares: r.big(9), RedeemReserve: r.big(10), IdleAssets: r.big(11),
		CoreSpot: r.big(12), CorePerpEquity: r.int(13), CorePerpWithdrawable: r.big(14), InFlightToCore: r.big(15),
		PendingKind: TransferKind(r.u8(16)), PendingAmount: r.big(17), PendingAction: r.u8(18), PendingSentAt: r.u64(19),
		CoreAccountExists: r.bool(20), CoreLinkProven: r.bool(21), DeployLimit: r.big(22), QuotaMode: r.bool(23), Paused: r.bool(24),
		APIWallet: r.addr(25), Manager: r.addr(26), ManagerShares: r.big(27),
	}
	if r.err != nil {
		return nil, fmt.Errorf("vaultState: %w", r.err)
	}
	return s, nil
}

// DecodeUserState decodes VaultLens.userState (6 words).
func DecodeUserState(b []byte) (*UserState, error) {
	w, err := evm.ExpectWords(b, userStateWords)
	if err != nil {
		return nil, fmt.Errorf("userState: %w", err)
	}
	r := &wordReader{w: w}
	u := &UserState{
		Shares: r.big(0), PendingDepositAssets: r.big(1), ClaimableShares: r.big(2),
		PendingRedeemShares: r.big(3), ClaimableAssets: r.big(4), MayDeposit: r.bool(5),
	}
	if r.err != nil {
		return nil, fmt.Errorf("userState: %w", r.err)
	}
	return u, nil
}

func decodeEpochWords(w evm.Words) (EpochData, error) {
	r := &wordReader{w: w}
	e := EpochData{
		SettledAt: r.u64(0), SharePrice: r.big(1), TotalAssetsAfter: r.big(2), TotalSupplyAfter: r.big(3),
		DepositAssets: r.big(4), DepositShares: r.big(5), RedeemSharesPending: r.big(6),
		RedeemSharesFulfilled: r.big(7), RedeemAssets: r.big(8), FeeShares: r.big(9),
	}
	return e, r.err
}

// DecodeEpochData decodes epochData(e) (10 words) and stamps the epoch.
func DecodeEpochData(epoch uint64, b []byte) (*EpochData, error) {
	w, err := evm.ExpectWords(b, epochDataWords)
	if err != nil {
		return nil, fmt.Errorf("epochData: %w", err)
	}
	e, err := decodeEpochWords(w)
	if err != nil {
		return nil, fmt.Errorf("epochData: %w", err)
	}
	e.Epoch = epoch
	return &e, nil
}

// DecodeEpochRange decodes VaultLens.epochRange(vault, from, to): a dynamic
// array of epochData tuples. Element k is epoch from+k. The length must be
// exactly 32 + 32 + n×320 bytes.
func DecodeEpochRange(from uint64, b []byte) ([]EpochData, error) {
	w, err := evm.NewWords(b)
	if err != nil {
		return nil, fmt.Errorf("epochRange: %w", err)
	}
	if w.Len() < 2 {
		return nil, errors.New("epochRange: too short")
	}
	els, err := w.TupleArray(0, epochDataWords)
	if err != nil {
		return nil, fmt.Errorf("epochRange: %w", err)
	}
	out := make([]EpochData, len(els))
	for k, el := range els {
		e, err := decodeEpochWords(el)
		if err != nil {
			return nil, fmt.Errorf("epochRange[%d]: %w", k, err)
		}
		e.Epoch = from + uint64(k)
		out[k] = e
	}
	return out, nil
}

// DecodePendingTransfer decodes pendingTransfer() (5 words inline).
func DecodePendingTransfer(b []byte) (*PendingTransfer, error) {
	w, err := evm.ExpectWords(b, pendingTransferWords)
	if err != nil {
		return nil, fmt.Errorf("pendingTransfer: %w", err)
	}
	r := &wordReader{w: w}
	p := &PendingTransfer{Kind: TransferKind(r.u8(0)), SentBlock: r.u64(1), SentAt: r.u64(2), Amount: r.big(3), SpotBefore: r.big(4)}
	if r.err != nil {
		return nil, fmt.Errorf("pendingTransfer: %w", r.err)
	}
	return p, nil
}

// DecodeString decodes a single dynamic string return (name(), symbol()).
func DecodeString(b []byte) (string, error) {
	w, err := evm.NewWords(b)
	if err != nil {
		return "", err
	}
	return w.String(0)
}
