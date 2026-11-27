package view

import "testing"

// A treasury withdrawal or parameter change whose votes meet the devnet's
// thresholds shows as ratified; one short of the committee's two thirds or
// the DReps' 67%, an info action, or a security-group change without the
// pools does not.
func TestRatifiedByTally(t *testing.T) {
	pass := map[string]Tally{
		classCommittee: {Yes: 2, No: 1},
		classDRep:      {Yes: 3, YesPower: 80, NoPower: 20},
	}
	shortCC := map[string]Tally{classCommittee: {Yes: 1, No: 2}, classDRep: pass[classDRep]}
	shortDRep := map[string]Tally{classCommittee: pass[classCommittee], classDRep: {Yes: 1, YesPower: 60, NoPower: 40}}
	withPools := map[string]Tally{classCommittee: pass[classCommittee], classDRep: pass[classDRep], classSPO: {Yes: 1, YesPower: 60, NoPower: 40}}
	for _, tc := range []struct {
		name string
		p    ProposalRow
		want bool
	}{
		{"treasury", ProposalRow{Type: "treasury_withdrawals", Tally: pass}, true},
		{"parameter", ProposalRow{Type: "parameter_change", Tally: pass}, true},
		{"info", ProposalRow{Type: "info", Tally: pass}, false},
		{"committee short", ProposalRow{Type: "parameter_change", Tally: shortCC}, false},
		{"DReps short", ProposalRow{Type: "parameter_change", Tally: shortDRep}, false},
		{"security without pools", ProposalRow{Type: "parameter_change", PoolsMayVote: true, Tally: pass}, false},
		{"security with pools", ProposalRow{Type: "parameter_change", PoolsMayVote: true, Tally: withPools}, true},
	} {
		if got := ratified(tc.p); got != tc.want {
			t.Errorf("%s: ratified = %t, want %t", tc.name, got, tc.want)
		}
	}
}
