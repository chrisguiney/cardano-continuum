package explorer

import (
	"math/big"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

func govAddr(t *testing.T, b byte) common.Address {
	t.Helper()
	a, err := common.NewAddressFromBytes(append([]byte{0xe0}, fill(b, 28)...))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func govProc(t *testing.T, act common.GovAction) common.ProposalProcedure {
	t.Helper()
	p, err := conway.NewConwayProposalProcedure(100_000_000_000, govAddr(t, 0x51), act, common.GovAnchor{Url: "https://example.test/a", DataHash: [32]byte(fill(0x12, 32))})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ptr[T any](v T) *T { return &v }

func rat(n, d int64) *cbor.Rat { return &cbor.Rat{Rat: big.NewRat(n, d)} }

func curParams() *conway.ConwayProtocolParameters {
	return &conway.ConwayProtocolParameters{
		MaxTxSize: 16384, MaxTxExUnits: common.ExUnits{Memory: 16_500_000, Steps: 10_000_000_000},
		DRepDeposit: 500_000_000, A0: rat(3, 10),
	}
}

func TestGovParameterChange(t *testing.T) {
	prev := common.GovActionId{TransactionId: [32]byte(fill(0x77, 32)), GovActionIdx: 2}
	upd := conway.ConwayProtocolParameterUpdate{
		MaxTxSize: ptr(uint(20_000)), MaxTxExUnits: &common.ExUnits{Memory: 20_000_000, Steps: 10_000_000_000},
		DRepDeposit: ptr(uint64(600_000_000)), A0: ptr(*rat(1, 4)),
	}
	act := &conway.ConwayParameterChangeGovAction{Type: uint(common.GovActionTypeParameterChange), ActionId: &prev, ParamUpdate: upd, PolicyHash: fill(0x31, 28)}
	g := WithCurrent(govDecode(govProc(t, act)), curParams())
	if g.Kind != "parameter_change" || g.Deposit != 100_000_000_000 || g.Anchor == nil || g.Anchor.URL != "https://example.test/a" ||
		g.Anchor.Hash != "12121212121212121212121212121212121212121212121212121212121212"+"12" {
		t.Fatalf("header %+v", g)
	}
	if g.Prev == nil || g.Prev.Index != 2 || g.Prev.Tx != "77777777777777777777777777777777777777777777777777777777777777"+"77" {
		t.Errorf("prev %+v", g.Prev)
	}
	if g.PolicyHash != "31313131313131313131313131313131313131313131313131313131" {
		t.Errorf("policy %q", g.PolicyHash)
	}
	want := map[string]ParamChange{
		"MaxTxSize":    {Name: "maxTxSize", Group: "network", Security: true, Unit: "bytes", New: "20,000", Current: "16,384"},
		"MaxTxExUnits": {Name: "maxTxExUnits", Group: "network", Unit: "memory units and CPU steps", New: "20,000,000 mem, 10,000,000,000 steps", Current: "16,500,000 mem, 10,000,000,000 steps"},
		"DRepDeposit":  {Name: "dRepDeposit", Group: "governance", Unit: "lovelace", New: "600,000,000", Current: "500,000,000"},
		"A0":           {Name: "a0", Group: "technical", Unit: "ratio", New: "0.25", Current: "0.3"},
	}
	if len(g.Params) != len(want) {
		t.Fatalf("params %+v", g.Params)
	}
	for _, p := range g.Params {
		w := want[p.Field]
		if p.Name != w.Name || p.Group != w.Group || p.Security != w.Security || p.Unit != w.Unit || p.New != w.New || p.Current != w.Current || p.Desc == "" {
			t.Errorf("%s: got %+v want %+v", p.Field, p, w)
		}
	}
	if got, want := Summary(g), "Change maxTxSize 16,384→20,000; 3 other params"; got != want {
		t.Errorf("summary %q want %q", got, want)
	}
}

func TestGovUnknownCurrent(t *testing.T) {
	act := &conway.ConwayParameterChangeGovAction{Type: uint(common.GovActionTypeParameterChange), ParamUpdate: conway.ConwayProtocolParameterUpdate{MaxTxExUnits: &common.ExUnits{Memory: 20_000_000, Steps: 1_000}}}
	g := govDecode(govProc(t, act))
	if len(g.Params) != 1 || g.Params[0].Current != "" || g.Params[0].Security {
		t.Fatalf("params %+v", g.Params)
	}
	if got, want := Summary(g), "Change maxTxExUnits to 20M mem"; got != want {
		t.Errorf("summary %q want %q", got, want)
	}
	// With the current value the summary shows the move, the example in the brief.
	g = WithCurrent(g, curParams())
	if got, want := Summary(g), "Change maxTxExUnits 16.5M→20M mem"; got != want {
		t.Errorf("summary %q want %q", got, want)
	}
}

func TestGovTreasury(t *testing.T) {
	a, b := govAddr(t, 0x61), govAddr(t, 0x62)
	act := &common.TreasuryWithdrawalGovAction{Type: uint(common.GovActionTypeTreasuryWithdrawal), Withdrawals: map[*common.Address]uint64{&a: 1_000_000_000, &b: 200_000_000}, PolicyHash: fill(0x32, 28)}
	g := govDecode(govProc(t, act))
	if g.Kind != "treasury_withdrawals" || len(g.Withdrawals) != 2 || g.Total != 1_200_000_000 || g.PolicyHash == "" {
		t.Fatalf("treasury %+v", g)
	}
	if !slices.IsSortedFunc(g.Withdrawals, func(x, y Withdrawal) int { return compareStr(x.Address, y.Address) }) {
		t.Errorf("withdrawals not sorted: %+v", g.Withdrawals)
	}
	if got, want := Summary(g), "Withdraw 1,200 ADA to 2 recipients"; got != want {
		t.Errorf("summary %q want %q", got, want)
	}
}

func TestGovCommittee(t *testing.T) {
	rm := common.Credential{CredType: common.CredentialTypeAddrKeyHash, Credential: common.CredentialHash(common.NewBlake2b224(fill(0x41, 28)))}
	add := common.Credential{CredType: common.CredentialTypeAddrKeyHash, Credential: common.CredentialHash(common.NewBlake2b224(fill(0x42, 28)))}
	act := &common.UpdateCommitteeGovAction{Type: uint(common.GovActionTypeUpdateCommittee), Credentials: []common.Credential{rm},
		CredEpochs: map[*common.Credential]uint64{&add: 250}, Quorum: cbor.Rat{Rat: big.NewRat(2, 3)}}
	g := govDecode(govProc(t, act))
	if g.Kind != "update_committee" || len(g.Removed) != 1 || len(g.Added) != 1 || g.Added[0].Epoch != 250 || g.Quorum != "2/3" {
		t.Fatalf("committee %+v", g)
	}
	if got, want := Summary(g), "Update committee: add 1, remove 1, quorum 2/3"; got != want {
		t.Errorf("summary %q want %q", got, want)
	}
}

func TestGovOtherKinds(t *testing.T) {
	c := &common.NewConstitutionGovAction{Type: uint(common.GovActionTypeNewConstitution)}
	c.Constitution.Anchor = common.GovAnchor{Url: "https://example.test/constitution", DataHash: [32]byte(fill(0x13, 32))}
	c.Constitution.ScriptHash = fill(0x33, 28)
	g := govDecode(govProc(t, c))
	if g.Constitution == nil || g.Constitution.Anchor.URL != "https://example.test/constitution" || g.Constitution.ScriptHash == "" {
		t.Errorf("constitution %+v", g.Constitution)
	}
	if got, want := Summary(g), "New constitution (https://example.test/constitution)"; got != want {
		t.Errorf("summary %q want %q", got, want)
	}

	h := &common.HardForkInitiationGovAction{Type: uint(common.GovActionTypeHardForkInitiation)}
	h.ProtocolVersion.Major, h.ProtocolVersion.Minor = 11, 0
	g = govDecode(govProc(t, h))
	if g.Version == nil || g.Version.Major != 11 || Summary(g) != "Hard fork to protocol version 11.0" {
		t.Errorf("hard fork %+v %q", g.Version, Summary(g))
	}

	n := &common.NoConfidenceGovAction{Type: uint(common.GovActionTypeNoConfidence)}
	if g = govDecode(govProc(t, n)); g.Kind != "no_confidence" || g.Prev != nil || Summary(g) != "No confidence in the committee" {
		t.Errorf("no confidence %+v %q", g, Summary(g))
	}
	i, _ := common.NewInfoGovAction()
	if g = govDecode(govProc(t, i)); g.Kind != "info" || Summary(g) != "Info action: a poll with no effect" {
		t.Errorf("info %+v %q", g, Summary(g))
	}
}

// The index keeps each proposal's decoded content after its block is
// evicted and drops it on rollback.
func TestIndexGovAction(t *testing.T) {
	x, _, _, rtx := fixtureChain(t, 100)
	g, err := x.GovAction(rtx, 0)
	if err != nil || g.Kind != "info" || g.Deposit != 100_000_000_000 || g.Tx != rtx {
		t.Fatalf("gov action %+v, %v", g, err)
	}
	if _, err := x.GovAction(rtx, 1); err == nil {
		t.Error("index 1 found")
	}

	x2, _, _, rtx2 := fixtureChain(t, 1)
	if g, err := x2.GovAction(rtx2, 0); err != nil || g.Kind != "info" {
		t.Errorf("after eviction %+v, %v", g, err)
	}
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// A proposal that crossed the wire decodes to the types govDecode reads.
func TestGovFromWire(t *testing.T) {
	a := govAddr(t, 0x61)
	upd := conway.ConwayProtocolParameterUpdate{MaxTxSize: ptr(uint(20_000))}
	pc := &conway.ConwayParameterChangeGovAction{Type: uint(common.GovActionTypeParameterChange), ParamUpdate: upd}
	tw := &common.TreasuryWithdrawalGovAction{Type: uint(common.GovActionTypeTreasuryWithdrawal), Withdrawals: map[*common.Address]uint64{&a: 5_000_000}}
	g := newGen(t, 7)
	x := newIndex(t, 100)
	var props []any
	for _, act := range []common.GovAction{pc, tw} {
		p := govProc(t, act)
		props = append(props, cbor.RawMessage(enc(t, p)))
	}
	raw := txSpec{body: map[uint]any{
		0: []any{[]any{fill(0x01, 32), uint32(0)}},
		1: []any{map[uint]any{0: shelleyAddr(0xB1), 1: uint64(3_000_000)}},
		2: uint64(300_000), 3: uint64(10_000), 20: props,
	}}.cbor(t)
	tx, err := conway.NewConwayTransactionFromCbor(raw)
	if err != nil {
		t.Fatal(err)
	}
	mustAdd(t, x, g.next())
	mustAdd(t, x, g.next(raw))
	hash := tx.Hash().String()
	p0, err := x.GovAction(hash, 0)
	if err != nil || p0.Kind != "parameter_change" || len(p0.Params) != 1 || p0.Params[0].New != "20,000" || !p0.Params[0].Security {
		t.Errorf("param change %+v, %v", p0, err)
	}
	p1, err := x.GovAction(hash, 1)
	if err != nil || p1.Kind != "treasury_withdrawals" || p1.Total != 5_000_000 || p1.Slot == 0 {
		t.Errorf("treasury %+v, %v", p1, err)
	}
	if err := x.Rollback(g.slotOf(0), g.hash(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := x.GovAction(hash, 0); err == nil {
		t.Error("proposal outlived its block's rollback")
	}
}
