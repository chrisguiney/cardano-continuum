package httpapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/ouroboros-mock/fixtures"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/explorer"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// xchain builds real Conway blocks for the explorer.
type xchain struct {
	t      *testing.T
	prev   common.Blake2b256
	number uint64
	slot   uint64
}

func simpleTx(t *testing.T) (raw []byte) {
	body, err := cbor.Encode(map[uint]any{
		0: []any{[]any{bytes.Repeat([]byte{1}, 32), uint32(0)}},
		1: []any{map[uint]any{0: append([]byte{0x60}, bytes.Repeat([]byte{9}, 28)...), 1: uint64(2_000_000)}},
		2: uint64(200_000),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = cbor.Encode([]any{cbor.RawMessage(body), map[uint]any{}, true, nil})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (c *xchain) next(prev *common.Blake2b256, issuer byte, txs ...[]byte) (cbor []byte, hash common.Blake2b256) {
	var gtxs []common.Transaction
	for _, raw := range txs {
		tx, err := conway.NewConwayTransactionFromCbor(raw)
		if err != nil {
			c.t.Fatal(err)
		}
		gtxs = append(gtxs, tx)
	}
	if prev == nil {
		prev = &c.prev
	}
	c.number++
	c.slot += 5
	b, err := fixtures.NewBlockBuilder(conway.EraConway).WithBlockNumber(c.number).WithSlot(c.slot).WithPreviousHash(*prev).
		WithIssuerVkey(common.IssuerVkey(bytes.Repeat([]byte{issuer}, 32))).WithProtocolVersion(10, 0).WithTransactions(gtxs...).Build()
	if err != nil {
		c.t.Fatal(err)
	}
	c.prev = b.Hash()
	return b.Cbor(), b.Hash()
}

type exTips map[string][]chain.Mark

func (f exTips) Tips() map[string][]chain.Mark { return f }

func xmark(height uint64, h common.Blake2b256) chain.Mark {
	return chain.Mark{Height: height, Point: chain.Point{Slot: height * 5, Hash: h}}
}

type explorerRig struct {
	t      *testing.T
	ex     *Explorer
	srv    *httptest.Server
	hashes []common.Blake2b256
	tx     string
}

func newRig(t *testing.T, tips Tips) *explorerRig {
	t.Helper()
	slots := testSlots(t)
	ex := NewExplorer(ExplorerOptions{Keep: 50, K: 2, Slots: &slots, Tips: tips, Limits: explorer.Limits{MaxMem: 1000, MaxSteps: 2000, MaxBytes: 90_112}})
	r := &explorerRig{t: t, ex: ex}
	c := &xchain{t: t}
	tx := simpleTx(t)
	gtx, _ := conway.NewConwayTransactionFromCbor(tx)
	r.tx = gtx.Hash().String()
	for i := range 3 {
		var txs [][]byte
		if i == 1 {
			txs = [][]byte{tx}
		}
		raw, h := c.next(nil, 7, txs...)
		r.hashes = append(r.hashes, h)
		r.feed(raw)
	}
	r.srv = httptest.NewServer(NewHandler(Config{Source: &fakeSource{hub: NewHub(8, 4, 8)}, Explorer: ex}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *explorerRig) feed(raw []byte) {
	r.t.Helper()
	blk, _ := conway.NewConwayBlockFromCbor(raw)
	content, err := eventlog.Encode(engine.BlockContent{Number: blk.BlockNumber(), Raw: raw})
	if err != nil {
		r.t.Fatal(err)
	}
	body, err := eventlog.Encode(eventlog.BlockApplied{Point: eventlog.ChainPoint{Slot: blk.SlotNumber(), Hash: blk.Hash().Bytes()}, Content: content})
	if err != nil {
		r.t.Fatal(err)
	}
	r.ex.Apply(eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindBlockApplied, Body: body})
}

func (r *explorerRig) get(path string, v any) int {
	r.t.Helper()
	resp, err := http.Get(r.srv.URL + path) //nolint:noctx // loopback test server
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if v != nil {
		_ = json.NewDecoder(resp.Body).Decode(v)
	}
	return resp.StatusCode
}

func TestExplorerBlocksAndParams(t *testing.T) {
	r := newRig(t, nil)
	var pg explorer.BlockPage
	if code := r.get("/api/explorer/blocks?limit=2", &pg); code != 200 || pg.Total != 3 || len(pg.Rows) != 2 || pg.Rows[0].Height != 3 {
		t.Fatalf("blocks %d %+v", code, pg)
	}
	for _, bad := range []string{"limit=0", "limit=101", "offset=-1", "bogus=1", "pool=zz", "limit=x"} {
		if code := r.get("/api/explorer/blocks?"+bad, nil); code != 400 {
			t.Errorf("?%s: %d, want 400", bad, code)
		}
	}
	if code := r.get("/api/explorer/blocks/"+hex.EncodeToString(bytes.Repeat([]byte{5}, 32)), nil); code != 404 {
		t.Errorf("unknown block: %d", code)
	}
	if code := r.get("/api/explorer/blocks/xyz", nil); code != 400 {
		t.Errorf("bad block key: %d", code)
	}
	if code := r.get("/api/explorer/txs/"+r.tx, nil); code != 200 {
		t.Errorf("tx: %d", code)
	}
	if code := r.get("/api/explorer/epochs/0", nil); code != 200 {
		t.Errorf("epoch: %d", code)
	}
	if code := r.get("/api/explorer/epochs/abc", nil); code != 400 {
		t.Errorf("bad epoch: %d", code)
	}
}

func TestExplorerForkIsVisible(t *testing.T) {
	var other common.Blake2b256
	other[0] = 0xEE
	tips := exTips{}
	r := newRig(t, tips)
	tips["p1"] = []chain.Mark{xmark(2, r.hashes[1]), xmark(3, r.hashes[2])}
	tips["p2"] = []chain.Mark{xmark(2, r.hashes[1]), xmark(3, r.hashes[2])}
	tips["p3"] = []chain.Mark{xmark(2, other), xmark(3, other)}
	var b BlockDetail
	if code := r.get("/api/explorer/blocks/2", &b); code != 200 {
		t.Fatalf("block: %d", code)
	}
	states := map[string]string{}
	for _, p := range b.Pools {
		states[p.Pool] = p.State
	}
	if states["p1"] != "on" || states["p2"] != "on" || states["p3"] != "fork" || !b.Fork {
		t.Fatalf("placement %+v fork=%v", b.Pools, b.Fork)
	}
	// The other pool's block is not in the index but resolves from its tip.
	var stub BlockDetail
	if code := r.get("/api/explorer/blocks/"+other.String(), &stub); code != 200 || !stub.Stub || stub.Height != 2 {
		t.Fatalf("stub %d %+v", code, stub)
	}
}

func TestExplorerSearchRouting(t *testing.T) {
	r := newRig(t, nil)
	tests := []struct {
		q, kind, route string
	}{
		{r.hashes[1].String(), "block", "/explorer/blocks/" + r.hashes[1].String()},
		{"2", "block", "/explorer/blocks/" + r.hashes[1].String()},
		{"slot:15", "block", "/explorer/blocks/" + r.hashes[2].String()},
		{r.tx, "tx", "/explorer/txs/" + r.tx},
		{"actor:12", "actor", "/actors/12"},
		{"@12", "actor", "/actors/12"},
	}
	for _, tt := range tests {
		var s SearchResult
		if code := r.get("/api/explorer/search?q="+tt.q, &s); code != 200 || string(s.Kind) != tt.kind || s.Route != tt.route {
			t.Errorf("search %q: %d %+v, want %s %s", tt.q, code, s, tt.kind, tt.route)
		}
	}
	for _, q := range []string{"", "nonsense", "99999", "addr_test1qqqqqqqq"} {
		if code := r.get("/api/explorer/search?q="+q, nil); code != 404 && code != 400 {
			t.Errorf("search %q: %d, want 404/400", q, code)
		}
	}
}

func TestExplorerFeedFollowsRollbackAndContext(t *testing.T) {
	r := newRig(t, nil)
	// A submitted decision names the transaction's actor and intent; the
	// submit result gives the verdict.
	sub, _ := eventlog.Encode(engine.Submitted{Intent: 9, ID: mustTxID(t, r.tx), Amount: 2_000_000, Kind: "pay"})
	res, _ := eventlog.Encode(engine.SubmitResult{Intent: 9, Verdict: engine.VerdictAccepted})
	r.ex.Apply(eventlog.Record{Seq: 10, Class: eventlog.Decision, Kind: engine.KindSubmitted, Actor: 4, Body: sub})
	r.ex.Apply(eventlog.Record{Seq: 11, Class: eventlog.Input, Kind: eventlog.KindSubmitResult, Body: res})
	var tx TxDetail
	if code := r.get("/api/explorer/txs/"+r.tx, &tx); code != 200 || tx.Continuum == nil || tx.Continuum.Intent != 9 || tx.Continuum.Verdict != "accepted" || tx.Continuum.Actor == nil || tx.Continuum.Actor.ID != 4 {
		t.Fatalf("tx context %d %+v", code, tx.Continuum)
	}
	// Roll back to block 1: the tx's block goes.
	to, _ := eventlog.Encode(eventlog.RolledBack{To: eventlog.ChainPoint{Slot: 5, Hash: r.hashes[0].Bytes()}})
	r.ex.Apply(eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindRolledBack, Body: to})
	if code := r.get("/api/explorer/txs/"+r.tx, nil); code != 404 {
		t.Fatalf("rolled-back tx: %d", code)
	}
	var st ExplorerStatus
	r.get("/api/explorer/status", &st)
	if st.Blocks != 1 || st.Tip != 1 {
		t.Fatalf("status %+v", st)
	}
	_ = context.Background
}

func TestExplorerOffWithoutIndex(t *testing.T) {
	srv := httptest.NewServer(NewHandler(Config{Source: &fakeSource{hub: NewHub(8, 4, 8)}}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/explorer/blocks") //nolint:noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("explorer route without an index: %d", resp.StatusCode)
	}
	var caps map[string]bool
	resp, _ = http.Get(srv.URL + "/api/capabilities") //nolint:noctx // loopback test server
	_ = json.NewDecoder(resp.Body).Decode(&caps)
	_ = resp.Body.Close()
	if caps["explorer"] {
		t.Fatal("capabilities claim an explorer")
	}
}

func testSlots(t *testing.T) sched.Slots {
	t.Helper()
	s, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustTxID(t *testing.T, h string) engine.TxID {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 32 {
		t.Fatalf("tx hash %q", h)
	}
	return engine.TxID(b)
}

func TestProjectionObserveSeesEveryRecord(t *testing.T) {
	p := NewProjection()
	var seqs []uint64
	p.Observe(func(r eventlog.Record) { seqs = append(seqs, r.Seq) })
	p.Apply(eventlog.Record{Seq: 1, Class: eventlog.Input, Kind: eventlog.KindTick})
	p.Apply(eventlog.Record{Seq: 2, Class: eventlog.Decision, Kind: "Unknown"})
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("observed %v", seqs)
	}
}

// forgingRig is three blocks by one issuer that node p3 (dingo) forged
// the last of, followed only by node p1 (cardano-node).
func forgingRig(t *testing.T) (*Explorer, common.Blake2b224, LogSourceConfig) {
	t.Helper()
	slots := testSlots(t)
	c := &xchain{t: t}
	var raws [][]byte
	var hashes []common.Blake2b256
	for range 3 {
		raw, h := c.next(nil, 7)
		raws = append(raws, raw)
		hashes = append(hashes, h)
	}
	blk, err := conway.NewConwayBlockFromCbor(raws[2])
	if err != nil {
		t.Fatal(err)
	}
	issuer := blk.IssuerVkey().Hash()

	follow := xmark(3, hashes[2])
	follow.Issuer = issuer
	tips := exTips{"p1": {follow}}
	pool := func(name, label string, impl v1alpha1.Implementation, last string) v1alpha1.ContinuumPool {
		p := v1alpha1.ContinuumPool{Spec: v1alpha1.ContinuumPoolSpec{PoolID: label, Implementation: impl, Tier: v1alpha1.TierForging}}
		p.Name, p.Status.LastBlock.Hash = name, last
		return p
	}
	kube := fakeKube{pools: []v1alpha1.ContinuumPool{
		pool("p1", "continuum-pool-1", v1alpha1.CardanoNode, ""),
		pool("p3", "continuum-pool-3", v1alpha1.Dingo, hashes[2].String()),
	}}
	ex := NewExplorer(ExplorerOptions{Keep: 50, K: 2, Slots: &slots, Kube: kube, Tips: tips})
	r := &explorerRig{t: t, ex: ex}
	for _, raw := range raws {
		r.feed(raw)
	}
	return ex, issuer, LogSourceConfig{Projection: NewProjection(), Kube: kube, Tips: tips}
}

// TestExplorerIssuerIsTheForgingNode: a block's issuer resolves to the node
// whose keys forged it (the node whose last own block it is), not to a node
// that merely follows a chain holding it. Every node follows every block,
// so labelling by follower named whichever node reported it.
func TestExplorerIssuerIsTheForgingNode(t *testing.T) {
	ex, _, _ := forgingRig(t)
	b, err := ex.Block(context.Background(), "3")
	if err != nil {
		t.Fatal(err)
	}
	if b.Pool == nil || b.Pool.Name != "p3" || b.Pool.Impl != "dingo" {
		t.Fatalf("block 3 issuer %s resolved to %+v, want p3 / dingo", b.Issuer, b.Pool)
	}
}

// TestSearchSeparatesNodesFromStakePools: a pool id opens the stake pool,
// in hex or bech32, and a node's name opens the node.
func TestSearchSeparatesNodesFromStakePools(t *testing.T) {
	ex, issuer, _ := forgingRig(t)
	ctx := context.Background()
	id := issuer.String()
	for _, tt := range []struct{ q, kind, route string }{
		{id, "pool", "/pools/" + id},
		{strings.ToUpper(id), "pool", "/pools/" + id},
		{view.PoolBech32(id), "pool", "/pools/" + id},
		{"p3", "node", "/nodes/p3"},
		{"p1", "node", "/nodes/p1"},
	} {
		got, err := ex.Search(ctx, tt.q)
		if err != nil || string(got.Kind) != tt.kind || got.Route != tt.route {
			t.Errorf("search %q: %+v %v, want %s %s", tt.q, got, err, tt.kind, tt.route)
		}
	}
	if _, err := ex.Search(ctx, strings.Repeat("ee", 28)); err == nil {
		t.Error("an unknown pool id resolved")
	}
}

// TestNodesAndStakePoolsCrossLink: the node running a pool's keys names the
// pool, and the pool's row lists that node with the blocks it issued; a
// node with no keys names none.
func TestNodesAndStakePoolsCrossLink(t *testing.T) {
	ex, issuer, cfg := forgingRig(t)
	srv := httptest.NewServer(NewHandler(Config{Source: NewLogSource(cfg), Explorer: ex}))
	t.Cleanup(srv.Close)
	r := &explorerRig{t: t, srv: srv}

	var nv NodesView
	if code := r.get("/api/nodes", &nv); code != 200 || len(nv.Nodes) != 2 {
		t.Fatalf("nodes %d %+v", code, nv)
	}
	if p1, p3 := nv.Nodes[0], nv.Nodes[1]; p1.StakePool != "" || p3.StakePool != issuer.String() {
		t.Fatalf("keys p1 %q p3 %q, want none and %s", p1.StakePool, p3.StakePool, issuer)
	}

	var sp StakePoolsView
	if code := r.get("/api/pools", &sp); code != 200 || len(sp.Pools) != 1 {
		t.Fatalf("pools %d %+v", code, sp)
	}
	p := sp.Pools[0]
	if p.ID != issuer.String() || p.Blocks == nil || *p.Blocks != 3 || len(p.Nodes) != 1 || p.Nodes[0].Name != "p3" || p.Nodes[0].Impl != "dingo" {
		t.Fatalf("pool %+v", p)
	}

	// Without an issuer on any followed chain, the index's copy of p3's
	// last block still names its keys.
	cfg.Tips.(exTips)["p1"][0].Issuer = common.Blake2b224{}
	if code := r.get("/api/nodes", &nv); code != 200 || nv.Nodes[1].StakePool != issuer.String() {
		t.Fatalf("keys from the index: %d %+v", code, nv.Nodes)
	}
}

// TestNodesCountForgedAndVerified: a node counts the chain's blocks its keys
// issued as forged and the rest, up to its tip, as verified; a node without
// keys has forged none, and one with no known tip height has neither count.
func TestNodesCountForgedAndVerified(t *testing.T) {
	ex, _, cfg := forgingRig(t)
	tips := cfg.Tips.(exTips)
	own := tips["p1"][0]
	tips["p3"] = []chain.Mark{own}
	tips["p1"] = []chain.Mark{{Height: 2, Point: chain.Point{Slot: own.Slot - 1}}}
	srv := httptest.NewServer(NewHandler(Config{Source: NewLogSource(cfg), Explorer: ex}))
	t.Cleanup(srv.Close)
	r := &explorerRig{t: t, srv: srv}

	var nv NodesView
	if code := r.get("/api/nodes", &nv); code != 200 || len(nv.Nodes) != 2 {
		t.Fatalf("nodes %d %+v", code, nv)
	}
	p1, p3 := nv.Nodes[0], nv.Nodes[1]
	if p1.Forged == nil || *p1.Forged != 0 || *p1.Verified != 2 {
		t.Fatalf("p1 (no keys, tip 2) %v %v", p1.Forged, p1.Verified)
	}
	if p3.Forged == nil || *p3.Forged != 3 || *p3.Verified != 0 {
		t.Fatalf("p3 (all three blocks) %v %v", p3.Forged, p3.Verified)
	}
}

func scriptRun(validator, instance string, ok bool, used, declared int64, why string) contract.Run {
	return contract.Run{
		Validator: validator, Instance: instance, Purpose: contract.Spend, Script: "ab",
		Declared: contract.Units{Mem: declared, Steps: declared * 10}, Used: contract.Units{Mem: used, Steps: used * 10},
		OK: ok, Error: why,
	}
}

func scriptSubmitted(t *testing.T, seq uint64, at int64, id byte, runs ...contract.Run) eventlog.Record {
	t.Helper()
	var tx [32]byte
	tx[0] = id
	body, err := eventlog.Encode(engine.Submitted{Intent: seq, ID: tx, Kind: "unlock", Validator: runs[0].Instance, Scripts: runs})
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{Seq: seq, At: sched.Time(at), Class: eventlog.Decision, Kind: engine.KindSubmitted, Actor: 1, Body: body}
}

func rowOf(t *testing.T, v ContractsView, name string) ValidatorRow {
	t.Helper()
	for _, r := range v.Validators {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no validator %s in %+v", name, v.Validators)
	return ValidatorRow{}
}

func TestContractsViewCountsRuns(t *testing.T) {
	t.Parallel()
	f := newScriptFold()
	f.apply(scriptSubmitted(t, 1, 1000, 1, scriptRun("vesting", "vesting#1", true, 100, 120, "")))
	f.apply(scriptSubmitted(t, 2, 2000, 2, scriptRun("vesting", "vesting#3", true, 300, 360, "")))
	f.apply(scriptSubmitted(t, 3, 3000, 3, scriptRun("vesting", "vesting#1", false, 500, 400, "declared units below the units used")))
	// A resend of the same bytes is the same transaction: not a new run.
	f.apply(scriptSubmitted(t, 4, 4000, 3, scriptRun("vesting", "vesting#1", false, 500, 400, "declared units below the units used")))
	dep, err := eventlog.Encode(engine.Deployed{Contract: 5, Address: "vesting#5", Validator: "vesting", Script: "cd"})
	if err != nil {
		t.Fatal(err)
	}
	f.apply(eventlog.Record{Seq: 5, Class: eventlog.Decision, Kind: engine.KindDeployed, Body: dep})

	v := f.view()
	if v.Toolchain != contract.Toolchain || len(v.Validators) != len(contract.All()) {
		t.Fatalf("view %q with %d validators", v.Toolchain, len(v.Validators))
	}
	r := rowOf(t, v, "vesting")
	if r.Runs != 3 || r.Failures != 1 || r.Instances != 3 {
		t.Fatalf("runs %d failures %d instances %d", r.Runs, r.Failures, r.Instances)
	}
	if r.Used.AvgMem != 300 || r.Used.MaxMem != 500 || r.Declared.MaxMem != 400 || r.Used.MaxSteps != 5000 {
		t.Fatalf("units used %+v declared %+v", r.Used, r.Declared)
	}
	if len(r.Reasons) != 1 || r.Reasons[0].Count != 1 || r.Reasons[0].Reason != "declared units below the units used" {
		t.Fatalf("reasons %+v", r.Reasons)
	}
	if len(r.Last) != 3 || r.Last[0].At != 3000 || r.Last[0].OK || r.Last[0].Instance != "vesting#1" || len(r.Last[0].Tx) != 64 {
		t.Fatalf("last runs, newest first: %+v", r.Last)
	}
	if m := rowOf(t, v, "free_mint"); m.Runs != 0 || m.Size == 0 || m.Language != "plutus_v3" || m.Hash == "" || len(m.Last) != 0 || m.Reasons == nil || m.Last == nil {
		t.Fatalf("an unused validator %+v", m)
	}
}

func TestContractsViewKeepsTheLastRuns(t *testing.T) {
	t.Parallel()
	f := newScriptFold()
	for i := range byte(lastRuns + 5) {
		f.apply(scriptSubmitted(t, uint64(i)+1, int64(i), i+1, scriptRun("escrow", "escrow#2", true, 1, 2, "")))
	}
	r := rowOf(t, f.view(), "escrow")
	if r.Runs != lastRuns+5 || len(r.Last) != lastRuns || r.Last[0].At != lastRuns+4 {
		t.Fatalf("runs %d, kept %d, newest at %d", r.Runs, len(r.Last), r.Last[0].At)
	}
}

func TestContractsEndpointAndTxScripts(t *testing.T) {
	r := newRig(t, nil)
	run := scriptRun("always_succeeds", "", true, 10, 12, "")
	sub, _ := eventlog.Encode(engine.Submitted{Intent: 9, ID: mustTxID(t, r.tx), Kind: "unlock", Validator: "always_succeeds", Scripts: []contract.Run{run}})
	r.ex.Apply(eventlog.Record{Seq: 10, Class: eventlog.Decision, Kind: engine.KindSubmitted, Actor: 4, Body: sub})

	var tx TxDetail
	if code := r.get("/api/explorer/txs/"+r.tx, &tx); code != 200 || tx.Continuum == nil || tx.Continuum.Validator != "always_succeeds" || len(tx.Continuum.Scripts) != 1 || tx.Continuum.Scripts[0].Used.Mem != 10 {
		t.Fatalf("tx context %d %+v", code, tx.Continuum)
	}

	var v ContractsView
	if code := r.get("/api/contracts", &v); code != 200 || rowOf(t, v, "always_succeeds").Runs != 1 {
		t.Fatalf("contracts %d %+v", code, v)
	}
}
