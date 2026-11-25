package core

import (
	"context"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

type exposureSourceStub struct {
	hlpxyz.API
	addresses []string
}

func (s *exposureSourceStub) IndexExposure(_ context.Context, addresses []string) (hlpxyz.IndexExposure, error) {
	s.addresses = addresses
	return hlpxyz.IndexExposure{Complete: true, Members: []hlpxyz.IndexExposureMember{{Address: "live", AsOf: 4000, Complete: true, Observations: []hlpxyz.IndexExposurePoint{{T: 1000}, {T: 2000}, {T: 3000}, {T: 4000}}}}}, nil
}

func TestIndexExposureSamplingPreservesDatesAndHistoricalMembership(t *testing.T) {
	s := &exposureSourceStub{}
	env := &Env{Now: func() time.Time { return time.UnixMilli(4000) }}
	ix := hlpxyz.Index{Def: hlpxyz.IndexDef{PreAllocs: []hlpxyz.IndexAlloc{{TS: 500, Weights: map[string]float64{"past": 1}}}}, Allocs: []hlpxyz.IndexAlloc{
		{TS: 1000, Weights: map[string]float64{"live": 1, "ignored": 0}},
		{TS: 5000, Weights: map[string]float64{"future": 1}},
	}}
	d := loadIndexExposure(context.Background(), env, s, ix, &IndexInput{Exposure: true, Points: 2})
	if len(s.addresses) != 2 || s.addresses[0] != "live" || s.addresses[1] != "past" {
		t.Fatalf("member scope: %v", s.addresses)
	}
	if !d.Complete || !d.Sampled || len(d.Members) != 1 || len(d.Members[0].Observations) != 2 || d.Members[0].Observations[0].T != 1000 || d.Members[0].Observations[1].T != 4000 || d.Members[0].AsOf != 4000 {
		t.Fatalf("observation dates altered: %+v", d)
	}
	if len(d.Warnings) == 0 {
		t.Fatal("exposure lost non-index-weighted limitation")
	}
	// A later display window must not move old observations to its boundary.
	env.Now = func() time.Time { return time.UnixMilli(10 * 86400000) }
	d = loadIndexExposure(context.Background(), env, s, ix, &IndexInput{Exposure: true, Points: 2, Window: "1d"})
	if len(d.Members[0].Observations) != 0 || d.Members[0].AsOf != 4000 {
		t.Fatalf("old source date promoted into current window: %+v", d)
	}
}
