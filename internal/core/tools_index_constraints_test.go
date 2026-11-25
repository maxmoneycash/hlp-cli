package core

import (
	"context"
	"testing"
	"time"

	"github.com/maxmoneycash/hlp-cli/internal/hlpxyz"
)

type constraintSourceStub struct {
	hlpxyz.API
	addresses []string
}

func (s *constraintSourceStub) IndexConstraints(_ context.Context, addresses []string) (hlpxyz.IndexConstraints, error) {
	s.addresses = addresses
	return hlpxyz.IndexConstraints{Complete: true, Sources: map[string]hlpxyz.IndexConstraintSource{}, Members: []hlpxyz.IndexConstraintMember{{Address: "live", CapacityAsOf: 4000, Capacity: []hlpxyz.IndexCapacityPoint{{T: 1000}, {T: 2000}, {T: 3000}, {T: 4000}}}}}, nil
}

func TestIndexConstraintsSelectsHistoricalMembersAndPreservesObservationDates(t *testing.T) {
	s := &constraintSourceStub{}
	now := time.UnixMilli(4000)
	env := &Env{HLPXYZ: s, Now: func() time.Time { return now }}
	ix := hlpxyz.Index{Def: hlpxyz.IndexDef{PreAllocs: []hlpxyz.IndexAlloc{{TS: 500, Weights: map[string]float64{"past": 1}}}}, Allocs: []hlpxyz.IndexAlloc{
		{TS: 1000, Weights: map[string]float64{"live": 1, "ignored": 0}},
		{TS: 5000, Weights: map[string]float64{"future": 1}},
	}}
	d := loadIndexConstraints(context.Background(), env, s, ix, &IndexInput{Constraints: true, Points: 2})
	if len(s.addresses) != 2 || s.addresses[0] != "live" || s.addresses[1] != "past" {
		t.Fatalf("membership scope: %v", s.addresses)
	}
	if !d.Complete || !d.Sampled || len(d.Members) != 1 || len(d.Members[0].Capacity) != 2 || d.Members[0].Capacity[0].T != 1000 || d.Members[0].Capacity[1].T != 4000 || d.Members[0].CapacityAsOf != 4000 {
		t.Fatalf("observation dates altered: %+v", d)
	}
	if len(d.Warnings) == 0 {
		t.Fatal("source observations lost replay limitation")
	}
}
