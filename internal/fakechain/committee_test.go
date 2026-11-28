package fakechain

import (
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// committeeFixture is a fixture whose genesis committee is seat 0 of the
// fixture's key store.
func committeeFixture(t *testing.T) (*fixture, keys.Signer, keys.Signer) {
	t.Helper()
	f := newFixture(t, func(c *Config) {
		s, err := keys.New(rng.Seed{1}, testNetwork)
		if err != nil {
			t.Fatal(err)
		}
		cold, _ := s.CommitteeSigners(0)
		c.Committee = []CommitteeSeat{{Cold: common.Blake2b224(cold.KeyHash()), Expiry: 1000}}
	})
	cold, hot := f.store.CommitteeSigners(0)
	return f, cold, hot
}

// authCert is a Conway auth_committee_hot_cert: [14, cold, hot].
func authCert(cold, hot keys.Signer) []any {
	c, h := cold.KeyHash(), hot.KeyHash()
	return []any{14, []any{0, c[:]}, []any{0, h[:]}}
}

// hotVote is a voting_procedures map with one yes vote by hot on action
// tx#0. The map's keys are arrays, which a Go map cannot hold, so it is
// encoded by hand: {voter => {gov_action_id => voting_procedure}}.
func hotVote(t *testing.T, hot keys.Signer, tx common.Blake2b256) cbor.RawMessage {
	t.Helper()
	h := hot.KeyHash()
	voter, err := cbor.Encode([]any{common.VoterTypeConstitutionalCommitteeHotKeyHash, h[:]})
	if err != nil {
		t.Fatal(err)
	}
	action, err := cbor.Encode([]any{tx[:], 0})
	if err != nil {
		t.Fatal(err)
	}
	proc, err := cbor.Encode([]any{common.GovVoteYes, nil})
	if err != nil {
		t.Fatal(err)
	}
	out := append([]byte{0xa1}, voter...)
	out = append(out, 0xa1)
	out = append(out, action...)
	return append(out, proc...)
}

// voteTx spends in (worth coin) with certs and hot's vote in the body,
// witnessed by alice and extra.
func (f *fixture) voteTx(in Ref, coin uint64, certs []any, vote cbor.RawMessage, extra ...keys.Signer) txSpec {
	s := txSpec{
		ins:     []Ref{in},
		outs:    []out{{f.alice.Address(), 0}},
		fee:     -1,
		signers: []keys.Wallet{f.alice},
		extra:   extra,
		bodyHook: func(m map[uint]any) {
			if len(certs) > 0 {
				m[4] = certs
			}
			m[19] = vote
		},
	}
	return s.balanced(f.t, coin)
}

// A committee vote carrying its own hot-key authorisation is refused from
// the mempool: the MEMPOOL rule reads the committee state before the
// transaction's certificates apply.
func TestCommitteeVoteWithItsOwnAuthorisationRefused(t *testing.T) {
	f, cold, hot := committeeFixture(t)
	tx := f.voteTx(f.genesisRef(f.alice), testLovelace, []any{authCert(cold, hot)}, hotVote(t, hot, common.Blake2b256{7}), cold, hot)
	if cs := rejectClasses(t, f.submit(tx.build(t))); !hasClass(cs, MempoolFailure) {
		t.Fatalf("classes %v, want %s", cs, MempoolFailure)
	}
}

// Once the authorisation is in the mempool ahead of it, the vote passes the
// MEMPOOL rule: the mempool's state includes the transactions before it.
func TestCommitteeVoteAfterAuthorisationPassesMempoolRule(t *testing.T) {
	f, cold, hot := committeeFixture(t)
	auth := f.certTx(f.alice, 0, authCert(cold, hot))
	auth.extra = []keys.Signer{cold}
	auth = auth.balanced(t, testLovelace)
	id := f.mustSubmit(auth.build(t))
	tx := f.voteTx(Ref{TxID: id, Index: 0}, auth.outs[0].coin, nil, hotVote(t, hot, common.Blake2b256{7}), hot)
	err := f.submit(tx.build(t))
	if err == nil {
		return
	}
	if cs := rejectClasses(t, err); hasClass(cs, MempoolFailure) {
		t.Fatalf("classes %v name %s after the authorisation", cs, MempoolFailure)
	}
}
