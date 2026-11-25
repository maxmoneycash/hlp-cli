package evm

import (
	"errors"
	"fmt"
)

// Multicall3 is the canonical Multicall3 deployment (present on HyperEVM
// testnet 998 and mainnet 999). BatchCall uses aggregate3 so a whole read
// is one eth_call: public RPCs cap JSON-RPC batches (drpc's free tier at 3).
var Multicall3 = MustAddress("0xcA11bde05977b3631167028862bE2a173976CA11")

// SelAggregate3 is aggregate3((address,bool,bytes)[]).
var SelAggregate3 = MustSelector("0x82ad56cb")

func pad32(n int) int { return (n + 31) / 32 * 32 }

// EncodeAggregate3 builds aggregate3 calldata with allowFailure = true for
// every call.
func EncodeAggregate3(msgs []CallMsg) []byte {
	n := len(msgs)
	// element encodings: target, allowFailure, offset(0x60), len, data
	elems := make([][]byte, n)
	for i, m := range msgs {
		e := make([]byte, 0, 4*WordSize+pad32(len(m.Data)))
		t := AddressWord(m.To)
		b := BoolWord(true)
		off := Uint64Word(3 * WordSize)
		l := Uint64Word(uint64(len(m.Data)))
		e = append(e, t[:]...)
		e = append(e, b[:]...)
		e = append(e, off[:]...)
		e = append(e, l[:]...)
		e = append(e, m.Data...)
		e = append(e, make([]byte, pad32(len(m.Data))-len(m.Data))...)
		elems[i] = e
	}
	out := make([]byte, 0, 4+WordSize*(2+n))
	out = append(out, SelAggregate3[:]...)
	head := Uint64Word(WordSize)
	cnt := Uint64Word(uint64(n))
	out = append(out, head[:]...)
	out = append(out, cnt[:]...)
	pos := n * WordSize
	for _, e := range elems {
		w := Uint64Word(uint64(pos))
		out = append(out, w[:]...)
		pos += len(e)
	}
	for _, e := range elems {
		out = append(out, e...)
	}
	return out
}

// DecodeAggregate3 decodes (bool success, bytes returnData)[] into one
// CallResult per call; failed calls carry a *RevertError.
func DecodeAggregate3(b []byte, want int, names map[Selector]string) ([]CallResult, error) {
	w, err := NewWords(b)
	if err != nil {
		return nil, err
	}
	off, err := w.offset(0)
	if err != nil {
		return nil, err
	}
	arr := Words(w[off:])
	n, err := arr.Uint64(0)
	if err != nil {
		return nil, err
	}
	if int(n) != want {
		return nil, decodeErr("aggregate3 returned %d results for %d calls", n, want)
	}
	body := Words(arr[WordSize:])
	out := make([]CallResult, n)
	for i := 0; i < int(n); i++ {
		eo, err := body.offset(i)
		if err != nil {
			return nil, err
		}
		el := Words(body[eo:])
		ok, err := el.Bool(0)
		if err != nil {
			return nil, err
		}
		data, err := el.Bytes(1)
		if err != nil {
			return nil, err
		}
		switch {
		case !ok:
			out[i] = CallResult{Err: DecodeRevert(data, names)}
		case len(data) == 0:
			out[i] = CallResult{Err: ErrEmptyResult}
		default:
			out[i] = CallResult{Data: data}
		}
	}
	return out, nil
}

// errBatchLimited marks a JSON-RPC batch the provider refused for size.
type errBatchLimited struct{ max int }

func (e *errBatchLimited) Error() string { return fmt.Sprintf("provider limits batches to %d", e.max) }

var errMulticallUnavailable = errors.New("multicall unavailable")
