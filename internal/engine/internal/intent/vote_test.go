package intent

import (
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
)

// mix is the share of yes, no and abstain votes over many proposals.
func mix(class, typ string) [3]float64 {
	r := rand.New(rand.NewPCG(1, 2))
	var n [3]float64
	const props, votes = 2000, 5
	for i := range props {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(i))
		id := wire.Ref{Tx: wire.TxID(sha256.Sum256(b[:]))}
		for range votes {
			switch voteChoice(r, class, id, typ) {
			case common.GovVoteYes:
				n[0]++
			case common.GovVoteNo:
				n[1]++
			default:
				n[2]++
			}
		}
	}
	for i := range n {
		n[i] /= props * votes
	}
	return n
}

// Honest votes follow the mainnet mix mithril2pq measured: DReps about 69%
// yes, 24% no and 9% abstain, pools about 82% yes. A run's parameter
// changes are favoured, so a change and its revert can enact.
func TestVoteChoiceFollowsMainnetMix(t *testing.T) {
	d := mix(voterDRep, "info")
	if d[0] < 0.65 || d[0] > 0.73 || d[1] < 0.19 || d[1] > 0.28 || d[2] < 0.05 || d[2] > 0.12 {
		t.Errorf("DRep mix yes/no/abstain %.2f", d)
	}
	if s := mix(voterSPO, "info"); s[0] < 0.78 || s[0] > 0.86 {
		t.Errorf("SPO mix yes/no/abstain %.2f", s)
	}
	if c := mix(voterDRep, "parameter_change"); c[0] < 0.85 {
		t.Errorf("parameter change mix yes/no/abstain %.2f: want it favoured", c)
	}
}

// One proposal's votes agree: a proposal is favoured or contested for
// every voter, so most of a favoured one's votes are yes.
func TestVoteChoiceIsCorrelatedPerProposal(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	split := 0
	for i := range 200 {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(i))
		id := wire.Ref{Tx: wire.TxID(sha256.Sum256(b[:]))}
		yes := 0
		for range 20 {
			if voteChoice(r, voterDRep, id, "info") == common.GovVoteYes {
				yes++
			}
		}
		if yes > 6 && yes < 14 {
			split++
		}
	}
	if split > 20 {
		t.Errorf("%d of 200 proposals split their votes evenly: votes are not correlated", split)
	}
}
