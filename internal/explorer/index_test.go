package explorer

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

func newIndex(t *testing.T, keep int) *Index {
	t.Helper()
	slots := testSlots(t)
	return New(Config{Keep: keep, K: 2, Slots: &slots, Limits: Limits{MaxMem: 14_000_000, MaxSteps: 10_000_000_000, MaxBytes: 90_112}})
}

func mustAdd(t *testing.T, x *Index, raw []byte) {
	t.Helper()
	if err := x.Add(raw); err != nil {
		t.Fatal(err)
	}
}

// fixtureChain adds three blocks: a payment, the rich tx spending its first
// output, and an empty block. It returns the index and the txs' hashes.
func fixtureChain(t *testing.T, keep int) (*Index, *chainGen, string, string) {
	t.Helper()
	g := newGen(t, 7)
	x := newIndex(t, keep)
	genesis := fill(0x01, 32)
	p := pay(t, genesis, 0, 0xB1, 20_000_000)
	ptx, _ := conway.NewConwayTransactionFromCbor(p)
	mustAdd(t, x, g.next(p))
	r, _ := rich(t, ptx.Hash().Bytes())
	rtx, _ := conway.NewConwayTransactionFromCbor(r)
	mustAdd(t, x, g.next(r))
	mustAdd(t, x, g.next())
	return x, g, ptx.Hash().String(), rtx.Hash().String()
}

func TestBlocksLatestFirst(t *testing.T) {
	x, g, _, _ := fixtureChain(t, 100)
	pg := x.Blocks(BlockQuery{Page: Page{Limit: 2}})
	if pg.Total != 3 || len(pg.Rows) != 2 {
		t.Fatalf("total %d rows %d", pg.Total, len(pg.Rows))
	}
	if pg.Rows[0].Height != 3 || pg.Rows[0].Hash != g.hash(2) || pg.Rows[1].Height != 2 {
		t.Fatalf("rows not latest first: %+v", pg.Rows)
	}
	if pg.Rows[1].Txs != 1 || pg.Rows[1].Epoch != 0 || pg.Rows[1].Size == 0 {
		t.Fatalf("row 2: %+v", pg.Rows[1])
	}
	if pg.Rows[1].ExMem != 1000 || pg.Rows[1].ExSteps != 2000 {
		t.Fatalf("exunits %d/%d", pg.Rows[1].ExMem, pg.Rows[1].ExSteps)
	}
	if pg.Limits.MaxMem != 14_000_000 {
		t.Fatalf("limits %+v", pg.Limits)
	}
	next := x.Blocks(BlockQuery{Page: Page{Offset: 2, Limit: 2}})
	if len(next.Rows) != 1 || next.Rows[0].Height != 1 {
		t.Fatalf("second page: %+v", next.Rows)
	}
}

func TestBlocksPoolFilter(t *testing.T) {
	x, _, _, _ := fixtureChain(t, 100)
	issuer := x.Blocks(BlockQuery{Page: Page{Limit: 1}}).Rows[0].Issuer
	if got := x.Blocks(BlockQuery{Pool: issuer}).Total; got != 3 {
		t.Fatalf("by issuer: %d", got)
	}
	if got := x.Blocks(BlockQuery{Pool: "00"}).Total; got != 0 {
		t.Fatalf("other pool: %d", got)
	}
}

func TestBlockDetail(t *testing.T) {
	x, g, ptx, rtx := fixtureChain(t, 100)
	for _, key := range []string{g.hash(1), "2"} {
		b, err := x.Block(key)
		if err != nil {
			t.Fatal(err)
		}
		if b.Hash != g.hash(1) || b.Prev != g.hash(0) || b.Next != g.hash(2) || b.Header.ProtoMajor != 10 || b.Header.BodyHash == "" || b.Header.VRFKey == "" || b.Header.OpCertSeq != 0 {
			t.Fatalf("block %s: %+v", key, b)
		}
		if len(b.Txs) != 1 || b.Txs[0].Hash != rtx || b.Txs[0].Fee != 300_000 {
			t.Fatalf("txs: %+v", b.Txs)
		}
	}
	if _, err := x.Block("deadbeef"); !errors.Is(err, ErrBadKey) {
		t.Fatalf("short key: %v", err)
	}
	if _, err := x.Block("99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown height: %v", err)
	}
	_ = ptx
}

func TestTxRichDecoding(t *testing.T) {
	x, g, ptx, rtx := fixtureChain(t, 100)
	tx, err := x.Tx(rtx)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Block != g.hash(1) || tx.Height != 2 || !tx.Valid || tx.Fee != 300_000 || tx.CborHex == "" || tx.Size == 0 {
		t.Fatalf("header: %+v", tx)
	}
	if tx.TTL == nil || *tx.TTL != 10_000 {
		t.Fatalf("ttl %v", tx.TTL)
	}
	if len(tx.Inputs) != 1 || tx.Inputs[0].Tx != ptx || tx.Inputs[0].Resolved == nil || tx.Inputs[0].Resolved.Lovelace != 20_000_000 {
		t.Fatalf("input not resolved: %+v", tx.Inputs)
	}
	if len(tx.Outputs) != 1 || tx.Outputs[0].Lovelace != 3_000_000 || tx.Outputs[0].Address == "" {
		t.Fatalf("outputs: %+v", tx.Outputs)
	}
	kinds := []string{}
	for _, c := range tx.Certs {
		kinds = append(kinds, c.Kind)
	}
	want := []string{"stake_delegation", "pool_retirement", "drep_registration"}
	if len(kinds) != 3 || kinds[0] != want[0] || kinds[1] != want[1] || kinds[2] != want[2] {
		t.Fatalf("certs %v", kinds)
	}
	if tx.Certs[0].Pool == "" || tx.Certs[2].Amount != 500_000_000 {
		t.Fatalf("cert fields: %+v", tx.Certs)
	}
	if len(tx.Mint) != 1 || tx.Mint[0].Quantity != "7" || tx.Mint[0].NameText != "tok" {
		t.Fatalf("mint: %+v", tx.Mint)
	}
	if len(tx.Votes) != 1 || tx.Votes[0].Choice != "yes" || tx.Votes[0].Voter != "drep_key" {
		t.Fatalf("votes: %+v", tx.Votes)
	}
	if len(tx.Proposals) != 1 || tx.Proposals[0].Kind != "info" || tx.Proposals[0].Deposit != 100_000_000_000 {
		t.Fatalf("proposals: %+v", tx.Proposals)
	}
	if len(tx.Redeemers) != 1 || tx.Redeemers[0].Tag != "spend" || tx.Redeemers[0].Mem != 1000 || tx.Redeemers[0].Steps != 2000 {
		t.Fatalf("redeemers: %+v", tx.Redeemers)
	}
	if len(tx.Scripts) != 1 || tx.Scripts[0].Language != "plutus_v2" || len(tx.Scripts[0].Hash) != 56 {
		t.Fatalf("scripts: %+v", tx.Scripts)
	}
	if len(tx.Signers) != 1 || len(tx.RefInputs) != 1 {
		t.Fatalf("signers %v refs %v", tx.Signers, tx.RefInputs)
	}
}

func TestTxInvalidFlag(t *testing.T) {
	g := newGen(t, 7)
	x := newIndex(t, 10)
	bad := txSpec{invalid: true, body: map[uint]any{
		0: []any{[]any{fill(0x01, 32), uint32(0)}},
		1: []any{map[uint]any{0: shelleyAddr(1), 1: uint64(2_000_000)}},
		2: uint64(200_000),
	}}.cbor(t)
	mustAdd(t, x, g.next(bad))
	b, err := x.Block("1")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := x.Tx(b.Txs[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Valid {
		t.Fatal("isValid=false not reported")
	}
}

func TestAddressView(t *testing.T) {
	x, _, ptx, rtx := fixtureChain(t, 100)
	addr, _ := common.NewAddressFromBytes(shelleyAddr(0xB1))
	v, err := x.Address(addr.String(), Page{})
	if err != nil {
		t.Fatal(err)
	}
	// 0xB1 received 20 ADA in the payment and 3 ADA from the rich tx; the
	// payment's output was spent by the rich tx.
	if v.Balance != 3_000_000 || len(v.Utxos) != 1 || v.Utxos[0].Tx != rtx {
		t.Fatalf("utxos: %+v", v)
	}
	if v.TxCount != 2 || len(v.History) != 2 || v.History[0].Hash != rtx || v.History[1].Hash != ptx {
		t.Fatalf("history: %+v", v.History)
	}
	if _, err := x.Address("not-an-address", Page{}); !errors.Is(err, ErrBadKey) {
		t.Fatalf("bad address: %v", err)
	}
}

func TestEpochSummary(t *testing.T) {
	x, _, _, _ := fixtureChain(t, 100)
	e, err := x.Epoch(0)
	if err != nil {
		t.Fatal(err)
	}
	if e.Blocks != 3 || e.Txs != 2 || e.Fees != 470_000 || len(e.Pools) != 1 || e.Pools[0].Blocks != 3 {
		t.Fatalf("epoch: %+v", e)
	}
	if _, err := x.Epoch(9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing epoch: %v", err)
	}
}

func TestRollback(t *testing.T) {
	x, g, ptx, rtx := fixtureChain(t, 100)
	if err := x.Rollback(g.slotOf(0), g.hash(0)); err != nil {
		t.Fatal(err)
	}
	if x.Blocks(BlockQuery{}).Total != 1 {
		t.Fatal("blocks not rolled back")
	}
	if _, err := x.Tx(rtx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back tx still found: %v", err)
	}
	// The payment's output is unspent again.
	addr, _ := common.NewAddressFromBytes(shelleyAddr(0xB1))
	v, _ := x.Address(addr.String(), Page{})
	if v.Balance != 20_000_000 || v.TxCount != 1 {
		t.Fatalf("address after rollback: %+v", v)
	}
	if e, _ := x.Epoch(0); e.Blocks != 1 || e.Txs != 1 {
		t.Fatalf("epoch after rollback: %+v", e)
	}
	_ = ptx
	if err := x.Rollback(1, g.hash(2)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rollback to unknown point: %v", err)
	}
}

func TestEvictionKeepsIndex(t *testing.T) {
	g := newGen(t, 7)
	x := newIndex(t, 3)
	var firstTx string
	prev := fill(0x01, 32)
	for i := range 10 {
		raw := pay(t, prev, 0, byte(i+1), 20_000_000)
		tx, _ := conway.NewConwayTransactionFromCbor(raw)
		if i == 0 {
			firstTx = tx.Hash().String()
		}
		prev = tx.Hash().Bytes()
		mustAdd(t, x, g.next(raw))
	}
	if n := x.FullBlocks(); n != 3 {
		t.Fatalf("full blocks retained: %d, want 3", n)
	}
	if x.Blocks(BlockQuery{}).Total != 10 {
		t.Fatal("compact index lost blocks")
	}
	old, err := x.Block("1")
	if err != nil || !old.Evicted || len(old.Txs) != 1 {
		t.Fatalf("evicted block: %+v, %v", old, err)
	}
	tx, err := x.Tx(firstTx)
	if err != nil || !tx.Evicted || tx.Height != 1 {
		t.Fatalf("evicted tx: %+v, %v", tx, err)
	}
	recent, err := x.Block("10")
	if err != nil || recent.Evicted {
		t.Fatalf("recent block: %+v, %v", recent, err)
	}
	if err := x.Rollback(g.slotOf(1), g.hash(1)); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("rollback past retained window: %v", err)
	}
}

func TestFindRoutes(t *testing.T) {
	x, g, ptx, rtx := fixtureChain(t, 100)
	addr, _ := common.NewAddressFromBytes(shelleyAddr(0xB1))
	tests := []struct {
		q    string
		kind Kind
		id   string
	}{
		{g.hash(1), KindBlock, g.hash(1)},
		{"2", KindBlock, g.hash(1)},
		{"slot:" + itoa(g.slotOf(2)), KindBlock, g.hash(2)},
		{rtx, KindTx, rtx},
		{ptx, KindTx, ptx},
		{addr.String(), KindAddress, addr.String()},
		{"  " + rtx + "\n", KindTx, rtx},
	}
	for _, tt := range tests {
		h, ok := x.Find(tt.q)
		if !ok || h.Kind != tt.kind || h.ID != tt.id {
			t.Errorf("Find(%q) = %+v, %v; want %s %s", tt.q, h, ok, tt.kind, tt.id)
		}
	}
	if _, ok := x.Find("0123456789012345678901234567890123456789012345678901234567890123"); ok {
		t.Error("unknown 64-hex matched")
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		q    string
		want Kind
	}{
		{"addr_test1qqqqqqqq", KindAddress},
		{"addr1qxyz", KindAddress},
		{"stake_test1uabc", KindAddress},
		{"pool1abcdef", KindPool},
		{"actor:12", KindActor},
		{"@12", KindActor},
		{"12", KindNumber},
		{"slot:12", KindSlot},
		{"deadbeef", KindUnknown},
		{"", KindUnknown},
	}
	for _, tt := range tests {
		if got := Classify(tt.q); got != tt.want {
			t.Errorf("Classify(%q) = %s, want %s", tt.q, got, tt.want)
		}
	}
	if Classify(string(fillHex(64))) != KindHash64 || Classify(string(fillHex(56))) != KindPool {
		t.Error("hex lengths: 64 is a block or tx hash, 56 a pool id")
	}
}

func TestPlacementShowsFork(t *testing.T) {
	tips := map[string][]Tip{
		"p1": {{Height: 5, Hash: "aa"}, {Height: 6, Hash: "bb"}},
		"p2": {{Height: 5, Hash: "aa"}, {Height: 6, Hash: "bb"}},
		"p3": {{Height: 5, Hash: "aa"}, {Height: 6, Hash: "cc"}},
		"p4": {{Height: 5, Hash: "aa"}},
	}
	got := Placement(6, "bb", tips)
	by := map[string]Placed{}
	for _, p := range got {
		by[p.Pool] = p
	}
	if !by["p1"].OnBlock || !by["p2"].OnBlock {
		t.Fatalf("p1/p2 should be on the block: %+v", got)
	}
	if by["p3"].OnBlock || by["p3"].Hash != "cc" || by["p3"].State != "fork" {
		t.Fatalf("p3 should be on a different block: %+v", by["p3"])
	}
	if by["p4"].State != "behind" {
		t.Fatalf("p4 has not reached the height: %+v", by["p4"])
	}
}

func TestSingleBlockReorderRejectsDisconnected(t *testing.T) {
	g := newGen(t, 7)
	x := newIndex(t, 10)
	mustAdd(t, x, g.next())
	g.next() // skipped: the next block does not connect
	if err := x.Add(g.next()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("disconnected block: %v", err)
	}
	if err := x.Add([]byte{0x80}); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestTxMetadataRendersAsJSON(t *testing.T) {
	g := newGen(t, 7)
	x := newIndex(t, 10)
	body := enc(t, map[uint]any{
		0: []any{[]any{fill(0x01, 32), uint32(0)}},
		1: []any{map[uint]any{0: shelleyAddr(1), 1: uint64(2_000_000)}},
		2: uint64(200_000),
	})
	aux := enc(t, map[uint]any{674: map[string]any{"msg": []any{"hello", uint64(7)}}})
	raw := enc(t, []any{cbor.RawMessage(body), map[uint]any{}, true, cbor.RawMessage(aux)})
	mustAdd(t, x, g.next(raw))
	b, _ := x.Block("1")
	tx, err := x.Tx(b.Txs[0].Hash)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(tx.Metadata)
	if string(js) != `{"674":{"msg":["hello",7]}}` {
		t.Fatalf("metadata json: %s", js)
	}
}
