package httpapi

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

func rewardAddr(t *testing.T, b byte) common.Address {
	t.Helper()
	a, err := common.NewAddressFromBytes(append([]byte{0xe0}, bytes.Repeat([]byte{b}, 28)...))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func govAnchor(path string, b byte) common.GovAnchor {
	return common.GovAnchor{Url: "https://example.test/" + path, DataHash: [32]byte(bytes.Repeat([]byte{b}, 32))}
}

// govKinds are the proposals the rig makes, in block order, with whether
// pools may vote on each (chain.MayVote).
var govKinds = []struct {
	name  string
	pools bool
	act   func(t *testing.T) common.GovAction
}{
	{"parameter_change_security", true, func(*testing.T) common.GovAction {
		return &conway.ConwayParameterChangeGovAction{Type: uint(common.GovActionTypeParameterChange),
			ActionId:    &common.GovActionId{TransactionId: [32]byte(bytes.Repeat([]byte{0x77}, 32)), GovActionIdx: 1},
			PolicyHash:  bytes.Repeat([]byte{0x31}, 28),
			ParamUpdate: conway.ConwayProtocolParameterUpdate{MaxTxSize: up(20_000), MaxTxExUnits: &common.ExUnits{Memory: 20_000_000, Steps: 10_000_000_000}}}
	}},
	{"parameter_change_other", false, func(*testing.T) common.GovAction {
		return &conway.ConwayParameterChangeGovAction{Type: uint(common.GovActionTypeParameterChange),
			ParamUpdate: conway.ConwayProtocolParameterUpdate{KeyDeposit: up(3_000_000), A0: &cbor.Rat{Rat: big.NewRat(1, 4)}}}
	}},
	{"treasury_withdrawals", false, func(t *testing.T) common.GovAction {
		a, b := rewardAddr(t, 0x61), rewardAddr(t, 0x62)
		return &common.TreasuryWithdrawalGovAction{Type: uint(common.GovActionTypeTreasuryWithdrawal), Withdrawals: map[*common.Address]uint64{&a: 1_000_000_000, &b: 200_000_000}}
	}},
	{"update_committee", true, func(*testing.T) common.GovAction {
		rm := common.Credential{CredType: common.CredentialTypeAddrKeyHash, Credential: common.NewBlake2b224(bytes.Repeat([]byte{0x41}, 28))}
		add := common.Credential{CredType: common.CredentialTypeScriptHash, Credential: common.NewBlake2b224(bytes.Repeat([]byte{0x42}, 28))}
		return &common.UpdateCommitteeGovAction{Type: uint(common.GovActionTypeUpdateCommittee), Credentials: []common.Credential{rm},
			CredEpochs: map[*common.Credential]uint64{&add: 250}, Quorum: cbor.Rat{Rat: big.NewRat(2, 3)}}
	}},
	{"new_constitution", false, func(*testing.T) common.GovAction {
		c := &common.NewConstitutionGovAction{Type: uint(common.GovActionTypeNewConstitution)}
		c.Constitution.Anchor, c.Constitution.ScriptHash = govAnchor("constitution", 0x13), bytes.Repeat([]byte{0x33}, 28)
		return c
	}},
	{"hard_fork_initiation", true, func(*testing.T) common.GovAction {
		h := &common.HardForkInitiationGovAction{Type: uint(common.GovActionTypeHardForkInitiation)}
		h.ProtocolVersion.Major, h.ProtocolVersion.Minor = 11, 0
		return h
	}},
	{"no_confidence", true, func(*testing.T) common.GovAction {
		return &common.NoConfidenceGovAction{Type: uint(common.GovActionTypeNoConfidence)}
	}},
	{"info", true, func(*testing.T) common.GovAction {
		i, _ := common.NewInfoGovAction()
		return i
	}},
}

type govRig struct {
	srv *httptest.Server
	ids map[string]string // kind to transaction hash
}

func txFrom(t *testing.T, body map[uint]any) (raw []byte, id engine.TxID) {
	t.Helper()
	raw, err := cbor.Encode([]any{cbor.RawMessage(mustEnc(t, body)), map[uint]any{}, true, nil})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conway.NewConwayTransactionFromCbor(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, engine.TxID(tx.Hash().Bytes())
}

func mustEnc(t *testing.T, v any) []byte {
	t.Helper()
	b, err := cbor.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// voteBody is a transaction body casting one vote on action. The votes map
// is keyed by an array, which Go cannot hold as a map key, so it is
// concatenated by hand.
func voteBody(t *testing.T, action engine.TxID, kind uint8, hash byte, vote uint8, anchor any) map[uint]any {
	t.Helper()
	votes := []byte{0xa1}
	votes = append(votes, mustEnc(t, []any{kind, append([]byte{hash}, make([]byte, 27)...)})...)
	votes = append(votes, 0xa1)
	votes = append(votes, mustEnc(t, []any{action[:], uint32(0)})...)
	votes = append(votes, mustEnc(t, []any{vote, anchor})...)
	return map[uint]any{
		0:  []any{[]any{bytes.Repeat([]byte{hash}, 32), uint32(0)}},
		1:  []any{map[uint]any{0: append([]byte{0x60}, bytes.Repeat([]byte{9}, 28)...), 1: uint64(2_000_000)}},
		2:  uint64(200_000),
		19: cbor.RawMessage(votes),
	}
}

func newGovRig(t *testing.T) *govRig {
	t.Helper()
	slots := testSlots(t)
	owner := store.Identity{Kind: "wallet", Agenda: "network", StakeAddr: rewardAddr(t, 0x51).String()}
	payee := store.Identity{Kind: "wallet", Agenda: "hodl", StakeAddr: rewardAddr(t, 0x61).String()}
	names := fakeNames{rows: []store.ActorRow{identRow(t, 5, "network-5", "active", owner), identRow(t, 6, "hodl-6", "active", payee)}}
	cur := &conway.ConwayProtocolParameters{
		MaxTxSize: 16384, KeyDeposit: 2_000_000, A0: &cbor.Rat{Rat: big.NewRat(3, 10)},
		MaxTxExUnits: common.ExUnits{Memory: 16_500_000, Steps: 10_000_000_000},
	}
	ex := NewExplorer(ExplorerOptions{Keep: 50, K: 2, Slots: &slots, Names: names, Params: cur})
	proj := NewProjection()
	seq := uint64(0)
	feed := func(r eventlog.Record) {
		seq++
		r.Seq = seq
		proj.Apply(r)
		ex.Apply(r)
	}
	c := &xchain{t: t}
	rig := &govRig{ids: map[string]string{}}
	var txs []engine.TxDigest
	var raws [][]byte
	for i, k := range govKinds {
		proc, err := conway.NewConwayProposalProcedure(100_000_000_000, rewardAddr(t, 0x51), k.act(t), govAnchor("proposal-"+k.name, byte(0x20+i)))
		if err != nil {
			t.Fatal(err)
		}
		raw, id := txFrom(t, map[uint]any{
			0:  []any{[]any{bytes.Repeat([]byte{1}, 32), uint32(i)}},
			1:  []any{map[uint]any{0: append([]byte{0x60}, bytes.Repeat([]byte{9}, 28)...), 1: uint64(2_000_000)}},
			2:  uint64(200_000),
			20: []any{cbor.RawMessage(mustEnc(t, proc))},
		})
		rig.ids[k.name] = hex.EncodeToString(id[:])
		raws = append(raws, raw)
		typ := strings.TrimSuffix(strings.TrimSuffix(k.name, "_security"), "_other")
		txs = append(txs, engine.TxDigest{ID: id, Proposals: 1, Gov: []engine.ProposalDigest{{Type: typ, Pools: k.pools, Deposit: 100_000_000_000}}})
		feed(submitted(t, sched.Time(i), 5, engine.Submitted{Intent: uint64(i), ID: id, Kind: "propose", Action: typ}))
	}
	cbor1, h1 := c.next(nil, 7, raws...)
	feed(blockRecord(t, 10_000, cbor1, h1, c.slot, txs))

	first, _ := hex.DecodeString(rig.ids["parameter_change_security"])
	pv, pid := txFrom(t, voteBody(t, engine.TxID(first), 4, 7, 1, []any{"https://example.test/why", bytes.Repeat([]byte{0x14}, 32)}))
	dv, did := txFrom(t, voteBody(t, engine.TxID(first), 2, 8, 0, nil))
	feed(submitted(t, 11_000, 7, engine.Submitted{Intent: 20, ID: pid, Kind: "vote", Voter: "spo", Choice: "yes", Power: 5000}))
	feed(submitted(t, 11_000, 8, engine.Submitted{Intent: 21, ID: did, Kind: "vote", Voter: "drep", Choice: "no", Power: 3000}))
	cbor2, h2 := c.next(nil, 7, pv, dv)
	feed(blockRecord(t, 12_000, cbor2, h2, c.slot, []engine.TxDigest{
		{ID: pid, Votes: []engine.VoteDigest{{Kind: 4, Hash: [28]byte{7}, Tx: engine.TxID(first), Choice: 1}}},
		{ID: did, Votes: []engine.VoteDigest{{Kind: 2, Hash: [28]byte{8}, Tx: engine.TxID(first), Choice: 0}}},
	}))

	src := NewLogSource(LogSourceConfig{Projection: proj, Slots: &slots, GovLifetime: 6, Names: names})
	rig.srv = httptest.NewServer(NewHandler(Config{Source: src, Explorer: ex}))
	t.Cleanup(rig.srv.Close)
	return rig
}

func blockRecord(t *testing.T, at sched.Time, raw []byte, h common.Blake2b256, slot uint64, txs []engine.TxDigest) eventlog.Record {
	t.Helper()
	content, err := eventlog.Encode(engine.BlockContent{Raw: raw, Txs: txs})
	if err != nil {
		t.Fatal(err)
	}
	body, err := eventlog.Encode(eventlog.BlockApplied{Point: eventlog.ChainPoint{Slot: slot, Hash: h.Bytes()}, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{At: at, Class: eventlog.Input, Kind: eventlog.KindBlockApplied, Body: body}
}

func TestGovDetailGoldens(t *testing.T) {
	rig := newGovRig(t)
	for _, k := range govKinds {
		code, body := get(t, rig.srv.URL+"/api/governance/"+rig.ids[k.name]+"/0")
		if code != 200 {
			t.Fatalf("%s: %d %s", k.name, code, body)
		}
		golden(t, "govdetail_"+k.name, bytes.ReplaceAll(body, []byte(rig.ids[k.name]), []byte("TX")))
	}
}

func TestGovDetailContent(t *testing.T) {
	rig := newGovRig(t)
	var d GovDetail
	code, body := get(t, rig.srv.URL+"/api/governance/"+rig.ids["parameter_change_security"]+"/0")
	if code != 200 || json.Unmarshal(body, &d) != nil {
		t.Fatalf("%d %s", code, body)
	}
	if d.Content == nil || len(d.Content.Params) != 2 || !d.Content.Params[0].Security || d.Content.Params[1].Security ||
		d.Content.Params[0].Current != "16,384" || d.Content.ReturnActor == nil || d.Content.ReturnActor.ID != 5 {
		t.Errorf("content %+v", d.Content)
	}
	if d.Summary != "Change maxTxSize 16,384→20,000; 1 other param" {
		t.Errorf("summary %q", d.Summary)
	}
	if len(d.Votes) != 2 || d.Votes[0].Voter == d.Votes[1].Voter {
		t.Fatalf("votes %+v", d.Votes)
	}
	for _, v := range d.Votes {
		if (v.Voter == "spo") != (v.Anchor != nil) {
			t.Errorf("anchor on %s vote: %+v", v.Voter, v.Anchor)
		}
	}
	if d.Proposal.Tally["spo"].YesPower != 5000 || d.Proposal.Tally["drep"].NoPower != 3000 {
		t.Errorf("tally %+v", d.Proposal.Tally)
	}

	// Pools may not vote on a treasury withdrawal; the recipient the run owns is linked.
	code, body = get(t, rig.srv.URL+"/api/governance/"+rig.ids["treasury_withdrawals"]+"/0")
	if code != 200 || json.Unmarshal(body, &d) != nil {
		t.Fatalf("%d %s", code, body)
	}
	if d.Voters[1].Class != "spo" || d.Voters[1].Allowed || !d.Voters[0].Allowed || !d.Voters[2].Allowed {
		t.Errorf("voters %+v", d.Voters)
	}
	if w := d.Content.Withdrawals; len(w) != 2 || w[0].Actor != nil || w[1].Actor == nil || w[1].Actor.ID != 6 {
		t.Errorf("withdrawals %+v", d.Content.Withdrawals)
	}
}

func TestGovDetailErrors(t *testing.T) {
	rig := newGovRig(t)
	for path, want := range map[string]int{
		"/api/governance/" + strings.Repeat("0", 64) + "/0":          404,
		"/api/governance/xyz/0":                                      400,
		"/api/governance/" + rig.ids["info"] + "/x":                  400,
		"/api/governance/" + rig.ids["info"] + "/0?bogus=1":          400,
		"/api/governance/" + strings.ToUpper(rig.ids["info"]) + "/0": 200,
	} {
		if code, body := get(t, rig.srv.URL+path); code != want {
			t.Errorf("%s: %d %s, want %d", path, code, body, want)
		}
	}
}

func TestGovernanceListSummaries(t *testing.T) {
	rig := newGovRig(t)
	var g GovernanceView
	code, body := get(t, rig.srv.URL+"/api/governance")
	if code != 200 || json.Unmarshal(body, &g) != nil {
		t.Fatalf("%d %s", code, body)
	}
	got := map[string]string{}
	for _, p := range g.Proposals {
		got[p.ID] = p.Summary
	}
	for kind, want := range map[string]string{
		"parameter_change_security": "Change maxTxSize 16,384→20,000; 1 other param",
		"parameter_change_other":    "Change keyDeposit 2M→3M; 1 other param",
		"treasury_withdrawals":      "Withdraw 1,200 ADA to 2 recipients",
		"update_committee":          "Update committee: add 1, remove 1, quorum 2/3",
		"new_constitution":          "New constitution (https://example.test/constitution)",
		"hard_fork_initiation":      "Hard fork to protocol version 11.0",
		"no_confidence":             "No confidence in the committee",
		"info":                      "Info action: a poll with no effect",
	} {
		if s := got[rig.ids[kind]+"#0"]; s != want {
			t.Errorf("%s: %q, want %q", kind, s, want)
		}
	}
}
func up(v uint) *uint { return &v }
