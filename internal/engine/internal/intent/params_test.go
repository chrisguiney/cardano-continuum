package intent

import (
	"math/big"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

var (
	passed = chain.Tally{DRepYes: 90, DRepTotal: 100, CommitteeYes: 3, CommitteeSeats: 3}
	failed = chain.Tally{DRepYes: 10, DRepTotal: 100, CommitteeYes: 3, CommitteeSeats: 3}
)

func gid(b byte) chain.GovID { return chain.GovID{Tx: chain.TxID{b}} }

// The proposer changes a parameter, then reverts it naming the change, then
// moves to the next parameter; a link that did not pass is proposed again
// from the same root, and nothing is proposed beside an open link.
func TestPlanParamsChangesThenReverts(t *testing.T) {
	first, ok := planParams(nil)
	if !ok || first.prev != nil || first.update.A0 == nil || first.update.A0.Cmp(big.NewRat(1, 4)) != 0 {
		t.Fatalf("first proposal %+v: want a0 to 1/4 after no root", first)
	}
	if _, ok := planParams([]paramLink{{id: gid(1)}}); ok {
		t.Fatal("proposed beside an open parameter change")
	}
	revert, ok := planParams([]paramLink{{id: gid(1), closed: true, tally: passed}})
	if !ok || revert.prev == nil || *revert.prev != gid(1) || revert.update.A0 == nil || revert.update.A0.Cmp(big.NewRat(3, 10)) != 0 {
		t.Fatalf("after the change enacted %+v: want a0 back to 3/10 after it", revert)
	}
	again, _ := planParams([]paramLink{{id: gid(1), closed: true, tally: failed}})
	if again.prev != nil || again.update.A0 == nil || again.update.A0.Cmp(big.NewRat(1, 4)) != 0 {
		t.Fatalf("after the change failed %+v: want it again after no root", again)
	}
	next, _ := planParams([]paramLink{{id: gid(1), closed: true, tally: passed}, {id: gid(2), closed: true, tally: passed}})
	if next.prev == nil || *next.prev != gid(2) || next.update.A0 != nil || next.update.Tau == nil {
		t.Fatalf("after change and revert %+v: want the next parameter after the revert", next)
	}
}

// A parameter-change belief follows the thresholds: no DRep stake, the
// committee short of two thirds, or the governance group's 75% for the
// DRep-activity step all fail.
func TestParamPassed(t *testing.T) {
	if paramPassed(chain.Tally{CommitteeYes: 3, CommitteeSeats: 3}, drepTechnical) {
		t.Error("passed with no DRep stake")
	}
	if paramPassed(chain.Tally{DRepYes: 90, DRepTotal: 100, CommitteeYes: 1, CommitteeNo: 2, CommitteeSeats: 3}, drepTechnical) {
		t.Error("passed with one seat of three")
	}
	at70 := chain.Tally{DRepYes: 70, DRepTotal: 100, CommitteeYes: 2, CommitteeSeats: 3}
	if !paramPassed(at70, drepTechnical) || paramPassed(at70, drepGov) {
		t.Error("70% should pass the technical group and fail the governance group")
	}
}
