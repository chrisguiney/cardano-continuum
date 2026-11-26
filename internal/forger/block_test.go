package forger

import (
	"encoding/hex"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/consensus"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

const (
	testKESSlots = 100
	testMaxEvo   = 60
	// The honest cert has counter 1 so a rewind to 0 exists.
	testCounter = 1
)

func testParams() Params {
	return Params{
		Magic:             42,
		SystemStart:       time.Unix(1_700_000_000, 0).UTC(),
		SlotLength:        time.Second,
		EpochLength:       200,
		SecurityParam:     10,
		ActiveSlots:       big.NewRat(1, 4),
		SlotsPerKESPeriod: testKESSlots,
		MaxKESEvolutions:  testMaxEvo,
		MaxBodySize:       4096,
		MaxHeaderSize:     1100,
		ProtoMajor:        10,
		GenesisHash:       [32]byte{0xab, 1, 2, 3},
		PoolStake:         1,
		TotalStake:        3,
	}
}

// testCreds issues pool 2's keys with the honest cert and every alternate
// cert a misbehaviour needs.
func testCreds(t *testing.T) *Credentials {
	t.Helper()
	store, err := keys.New(rng.Seed{7}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := store.Pool(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	if files[KESFile], err = pk.KESSKey.TextEnvelope(); err != nil {
		t.Fatal(err)
	}
	if files[VRFFile], err = pk.VRFSKey.TextEnvelope(); err != nil {
		t.Fatal(err)
	}
	certs := map[string][2]uint64{
		CertFile:             {testCounter, 0},
		certFile(CertRewind): {0, 0},
		certFile(CertLeap):   {testCounter + 5, 0},
		certFile(CertFuture): {testCounter, 50},
	}
	for name, c := range certs {
		raw, err := store.OpCert(2, 0, c[0], c[1])
		if err != nil {
			t.Fatal(err)
		}
		files[name] = []byte(`{"type":"NodeOperationalCertificate","description":"","cborHex":"` + hex.EncodeToString(raw) + `"}`)
	}
	c, err := ParseCredentials(files)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var testParent = Tip{Slot: 90, Number: 12, Hash: [32]byte{0xcc, 9}}

// leaderSlot is the first slot at or after from the pool leads.
func leaderSlot(t *testing.T, p Params, c *Credentials, nonce [32]byte, from uint64, want bool) uint64 {
	t.Helper()
	for s := from; s < from+2000; s++ {
		l, err := Leader(p, c, nonce, s)
		if err != nil {
			t.Fatal(err)
		}
		if l == want {
			return s
		}
	}
	t.Fatalf("no slot with leader=%v from %d", want, from)
	return 0
}

// verdict is every rule gouroboros says the block breaks, given the chain
// state before it: the header rules from consensus.HeaderValidator (which
// reports all of them) and the body rules from ledger.VerifyBlock.
func verdict(t *testing.T, p Params, b Block, nonce [32]byte, chainCounter uint64, parent Tip) []string {
	t.Helper()
	// The decoder checks the body hash itself unless told not to; the test
	// wants that verdict from VerifyBlock, beside the others.
	blk, err := ledger.NewBlockFromCbor(ledger.BlockTypeConway, b.CBOR, common.VerifyConfig{SkipBodyHashValidation: true})
	if err != nil {
		t.Fatalf("decode forged block: %v", err)
	}
	h, ok := blk.Header().(*conway.ConwayBlockHeader)
	if !ok {
		t.Fatalf("header is %T", blk.Header())
	}
	if h.Hash() != common.Blake2b256(b.Hash) {
		t.Fatalf("block hash %x, gouroboros says %s", b.Hash, h.Hash())
	}
	bodyCbor, err := ledger.GetHeaderBodyCbor(h)
	if err != nil {
		t.Fatal(err)
	}
	hv := consensus.NewHeaderValidator(consensus.NetworkConfig{
		SecurityParam:     p.SecurityParam,
		ActiveSlotCoeff:   common.GenesisRat{Rat: p.ActiveSlots},
		SlotsPerKESPeriod: p.SlotsPerKESPeriod,
		MaxKESEvolutions:  p.MaxKESEvolutions,
	})
	res := hv.ValidateHeader(&consensus.ValidateHeaderInput{
		Slot: h.Body.Slot, BlockNumber: h.Body.BlockNumber, PrevHash: h.Body.PrevHash.Bytes(),
		IssuerVkey: h.Body.IssuerVkey[:], VrfKey: h.Body.VrfKey,
		VrfProof: h.Body.VrfResult.Proof, VrfOutput: h.Body.VrfResult.Output,
		KesSignature: h.Signature, HeaderBodyCbor: bodyCbor,
		KesPeriod:            h.Body.Slot / p.SlotsPerKESPeriod,
		OpCertHotVkey:        h.Body.OpCert.HotVkey,
		OpCertSequenceNumber: uint64(h.Body.OpCert.SequenceNumber),
		OpCertKesPeriod:      uint64(h.Body.OpCert.KesPeriod),
		OpCertSignature:      h.Body.OpCert.Signature,
		OpCertCounterState:   &chainCounter,
		PrevSlot:             parent.Slot, PrevBlockNumber: parent.Number, PrevHeaderHash: parent.Hash[:],
		EpochNonce: nonce[:], PoolStake: p.PoolStake, TotalStake: p.TotalStake,
		RegisteredVrfKeyHash: common.Blake2b256Hash(h.Body.VrfKey).Bytes(),
	})
	var out []string
	for _, e := range res.Errors {
		// The validator reports a certificate that has not started from
		// both its period check and its signature check.
		if msg := "header: " + e.Error(); !slices.Contains(out, msg) {
			out = append(out, msg)
		}
	}
	_, _, _, _, err = ledger.VerifyBlock(blk, hex.EncodeToString(nonce[:]), p.SlotsPerKESPeriod, common.VerifyConfig{
		SkipTransactionValidation: true, SkipStakePoolValidation: true,
		MaxKesEvolutions: p.MaxKESEvolutions,
		ProtocolParameters: &conway.ConwayProtocolParameters{
			MaxBlockBodySize: uint(p.MaxBodySize), MaxBlockHeaderSize: uint(p.MaxHeaderSize),
		},
	})
	// VerifyBlock repeats the header's VRF, KES and opcert checks and stops
	// at the first failure; only what the header validator cannot see is
	// added from it.
	if err != nil && len(out) == 0 {
		out = append(out, "block: "+err.Error())
	}
	return out
}

func TestHonestBlockVerifies(t *testing.T) {
	p, c := testParams(), testCreds(t)
	nonce := p.GenesisHash
	slot := leaderSlot(t, p, c, nonce, 100, true)
	b, err := Assemble(p, c, nonce, Forge{Kind: Honest, Parent: testParent, Slot: slot, VRFSlot: slot})
	if err != nil {
		t.Fatal(err)
	}
	if got := verdict(t, p, b, nonce, testCounter, testParent); len(got) != 0 {
		t.Fatalf("honest block rejected: %v", got)
	}
	if b.Slot != slot || b.Number != testParent.Number+1 || b.Prev != testParent.Hash {
		t.Errorf("block = slot %d number %d prev %x", b.Slot, b.Number, b.Prev)
	}
}

// TestMisbehaviours plans each misbehaviour at a slot where it applies and
// requires gouroboros to reject the block for the one rule it targets, and
// for nothing else.
func TestMisbehaviours(t *testing.T) {
	p, c := testParams(), testCreds(t)
	nonce := p.GenesisHash
	lead := leaderSlot(t, p, c, nonce, 100, true)
	idle := leaderSlot(t, p, c, nonce, 100, false)
	tests := []struct {
		kind Kind
		slot uint64
		// counter is the pool's highest opcert counter on chain.
		counter uint64
		want    string
	}{
		{OpCertRewind, lead, testCounter, "header: operational certificate counter rolled back"},
		{OpCertLeap, lead, testCounter, "header: operational certificate counter skipped ahead"},
		{KESBeforeStart, lead, testCounter, "header: operational certificate KES period is in the future: current=1, opcert=50"},
		{KESExpired, leaderSlot(t, p, c, nonce, testKESSlots*testMaxEvo, true), testCounter, "header: operational certificate has expired: evolution=60, max=60"},
		{KESWrongPeriod, lead, testCounter, "header: KES signature verification failed"},
		{VRFBadProof, lead, testCounter, "header: VRF verification failed"},
		{VRFNotLeader, idle, testCounter, "header: VRF output does not satisfy leader eligibility"},
		{BodyHash, lead, testCounter, "block: "},
		{Oversize, lead, testCounter, "block: "},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			st := testState(t, p, c, nonce, tt.slot, tt.counter)
			fs := planOne(t, tt.kind, st)
			if len(fs) != 1 {
				t.Fatalf("planned %d forges, want 1: %+v", len(fs), fs)
			}
			f := fs[0]
			if f.Expect != Reject || f.Rule == "" {
				t.Errorf("forge expects %q for rule %q, want a rejection with a rule", f.Expect, f.Rule)
			}
			b, err := Assemble(p, c, nonce, f)
			if err != nil {
				t.Fatal(err)
			}
			got := verdict(t, p, b, nonce, tt.counter, testParent)
			if len(got) != 1 || !strings.HasPrefix(got[0], tt.want) {
				t.Fatalf("verdict = %q, want exactly one starting %q", got, tt.want)
			}
			t.Logf("%s: %s", tt.kind, got[0])
		})
	}
}

func TestBodyRulesNamed(t *testing.T) {
	p, c := testParams(), testCreds(t)
	nonce := p.GenesisHash
	lead := leaderSlot(t, p, c, nonce, 100, true)
	for kind, want := range map[Kind]string{BodyHash: "body hash", Oversize: "exceeds"} {
		st := testState(t, p, c, nonce, lead, testCounter)
		b, err := Assemble(p, c, nonce, planOne(t, kind, st)[0])
		if err != nil {
			t.Fatal(err)
		}
		got := verdict(t, p, b, nonce, testCounter, testParent)
		if len(got) != 1 || !strings.Contains(strings.ToLower(got[0]), want) {
			t.Errorf("%s verdict = %q, want one naming %q", kind, got, want)
		}
		if kind == Oversize && uint64(b.BodySize) <= p.MaxBodySize {
			t.Errorf("oversize body is %d bytes, limit %d", b.BodySize, p.MaxBodySize)
		}
	}
}

// TestFutureSlot: the block is valid in every rule gouroboros checks; what
// is wrong is only that its slot has not started.
func TestFutureSlot(t *testing.T) {
	p, c := testParams(), testCreds(t)
	nonce := p.GenesisHash
	st := testState(t, p, c, nonce, 100, testCounter)
	fs := planOne(t, FutureSlot, st)
	if len(fs) != 1 {
		t.Fatalf("planned %d forges", len(fs))
	}
	f := fs[0]
	if f.Slot < st.Slot+futureGap || p.Epoch(f.Slot) != p.Epoch(st.Slot) {
		t.Fatalf("future slot %d from slot %d", f.Slot, st.Slot)
	}
	b, err := Assemble(p, c, nonce, f)
	if err != nil {
		t.Fatal(err)
	}
	if got := verdict(t, p, b, nonce, testCounter, testParent); len(got) != 0 {
		t.Fatalf("future block breaks other rules: %v", got)
	}
}

func TestEquivocatePlan(t *testing.T) {
	p, c := testParams(), testCreds(t)
	nonce := p.GenesisHash
	lead := leaderSlot(t, p, c, nonce, 100, true)
	st := testState(t, p, c, nonce, lead, testCounter)
	fs := planOne(t, Equivocate, st)
	if len(fs) != 2 {
		t.Fatalf("planned %d forges, want 2", len(fs))
	}
	var blocks [2]Block
	for i, f := range fs {
		if f.Expect != Adopt {
			t.Errorf("forge %d expects %q: each block alone is valid", i, f.Expect)
		}
		b, err := Assemble(p, c, nonce, f)
		if err != nil {
			t.Fatal(err)
		}
		if got := verdict(t, p, b, nonce, testCounter, testParent); len(got) != 0 {
			t.Errorf("equivocating block %d is invalid: %v", i, got)
		}
		blocks[i] = b
	}
	if blocks[0].Slot != blocks[1].Slot || blocks[0].Hash == blocks[1].Hash {
		t.Errorf("want two different blocks in one slot, got %x@%d and %x@%d",
			blocks[0].Hash, blocks[0].Slot, blocks[1].Hash, blocks[1].Slot)
	}
	if len(fs[0].SendTo) == 0 || len(fs[1].SendTo) == 0 {
		t.Fatalf("both blocks need a peer set: %v %v", fs[0].SendTo, fs[1].SendTo)
	}
	for _, peer := range fs[0].SendTo {
		if slices.Contains(fs[1].SendTo, peer) {
			t.Errorf("peer %s receives both blocks", peer)
		}
	}
	st.Peers = st.Peers[:1]
	if fs := planOne(t, Equivocate, st); len(fs) != 1 || fs[0].Kind != Honest {
		t.Errorf("with one peer equivocation must fall back to an honest block, got %+v", fs)
	}
}

func testState(t *testing.T, p Params, c *Credentials, nonce [32]byte, slot, counter uint64) State {
	t.Helper()
	l, err := Leader(p, c, nonce, slot)
	if err != nil {
		t.Fatal(err)
	}
	return State{
		Slot: slot, Leader: l, Tip: testParent, Nonce: nonce,
		Counter: counter, Forged: true, Peers: []PeerID{"10.0.0.2", "10.0.0.3", "10.0.0.4"},
	}
}

// planOne plans with only kind enabled at rate 1.
func planOne(t *testing.T, kind Kind, st State) []Forge {
	t.Helper()
	p, c := testParams(), testCreds(t)
	pl, err := NewPlanner(p, c, Schedule{Seed: rng.Seed{1}, Rules: []Rule{{Kind: kind, Rate: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := pl.Plan(st)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestDeterminism(t *testing.T) {
	p, c := testParams(), testCreds(t)
	nonce := p.GenesisHash
	run := func(seed rng.Seed) string {
		var rules []Rule
		for _, k := range Kinds() {
			rules = append(rules, Rule{Kind: k, Rate: 0.3})
		}
		pl, err := NewPlanner(p, c, Schedule{Seed: seed, Rules: rules})
		if err != nil {
			t.Fatal(err)
		}
		var sb strings.Builder
		for slot := uint64(100); slot < 400; slot++ {
			fs, err := pl.Plan(testState(t, p, c, nonce, slot, testCounter))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range fs {
				b, err := Assemble(p, c, nonce, f)
				if err != nil {
					t.Fatal(err)
				}
				sb.WriteString(string(f.Kind) + " " + hex.EncodeToString(b.CBOR[:64]) + hex.EncodeToString(b.Hash[:]) + strings.Join(peerStrings(f.SendTo), ",") + "\n")
			}
		}
		return sb.String()
	}
	a, b := run(rng.Seed{1}), run(rng.Seed{1})
	if a != b {
		t.Fatal("the same seed planned different blocks")
	}
	if a == run(rng.Seed{2}) {
		t.Fatal("different seeds planned the same blocks")
	}
	for _, k := range []Kind{Equivocate, OpCertRewind, VRFNotLeader, FutureSlot} {
		if !strings.Contains(a, string(k)+" ") {
			t.Errorf("300 slots at rate 0.3 never planned %s", k)
		}
	}
}

func peerStrings(ps []PeerID) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}
