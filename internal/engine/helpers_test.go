package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// testProfileYAML is a busy wallet population: frequent acts, a high send
// weight, births, dormancy and abandonment, so a short run exercises every
// decision kind.
const testProfileYAML = `version: 1
profiles:
  - name: spender
    kind: wallet
    agenda: hodl
    traits:
      competence: beta(5, 3)
      patience: const(10)
      learning_rate: const(0.05)
      care: beta(2, 2)
    budget: 200000000
    rates:
      base:
        send: 0.4
        delegate: 0.1
      diurnal: 0.3
    weights:
      send: 0.9
      delegate: 0.1
    hazards:
      birth: 0.02
      dormancy: lognormal(3, 0.5)
      abandon: 0.0005
`

func testRegistry(t testing.TB) *profile.Registry {
	t.Helper()
	ps, err := profile.Parse(strings.NewReader(testProfileYAML))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := profile.NewRegistry(ps)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func buildParams() build.Params {
	pp := fakechain.DefaultParams()
	return build.Params{
		MinFeeA: uint64(pp.MinFeeA), MinFeeB: uint64(pp.MinFeeB),
		CoinsPerUtxoByte: uint64(pp.AdaPerUtxoByte), MaxTxSize: uint32(pp.MaxTxSize),
	}
}

func testConfig(t testing.TB, seed uint64, wallets int) Config {
	t.Helper()
	slots, err := sched.NewSlots(1000, 120)
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Seed: SeedFromInt(seed), Profiles: testRegistry(t), Cohorts: []Cohort{{Profile: "spender", Count: wallets}},
		Slots: slots, K: 3, BlockEvery: 4, Build: buildParams(), TTL: 30, BinaryVersion: "test",
	}
}

var errCrash = errors.New("injected crash")

// world is what survives an engine crash: the chain, the clock and the files.
type world struct {
	t     testing.TB
	cfg   Config
	chain *fakechain.Chain
	clock *sched.Virtual
	node  *FakeNode
	dir   string
	// log survives crashes, as a file would; kill drops what it had not
	// synced. A file log fsyncs every input, which makes crash sweeps slow.
	log *eventlog.MemLog
	// db stays open across crashes: a committed SQLite transaction survives a
	// kill, and reopening per crash would dominate the sweep's run time.
	db *store.DB
	// pods, when set, reports pod health to the engine; controls steer it.
	pods     PodSource
	controls ControlSource
	// wrap, when set, is the node the engine talks to in place of node;
	// monitor answers the view monitor's queries.
	wrap    func(*FakeNode) Node
	monitor Viewer
	// mem, when set, replaces the SQLite store: the crash sweep starts
	// hundreds of worlds and SQLite's fsyncs would dominate it.
	mem *memStore
}

func newWorld(t testing.TB, cfg Config, faults ...fakechain.Scheduled) *world {
	t.Helper()
	c, err := NewFakeChain(&cfg, faults...)
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, cfg: cfg, chain: c, clock: sched.NewVirtual(0),
		node: &FakeNode{Chain: c, Slots: cfg.Slots, BlockEvery: cfg.BlockEvery}, dir: t.TempDir(),
		log: &eventlog.MemLog{}}
}

// proc is one engine process: its open log and store and the runner.
type proc struct {
	log *eventlog.MemLog
	db  *store.DB
	r   *Runner
}

func (w *world) start(crash func(string) error, every int) (*proc, error) {
	ctx := context.Background()
	l := w.log
	var cp Checkpointer = w.mem
	if w.mem == nil && w.db == nil {
		db, err := store.Open(ctx, filepath.Join(w.dir, "state.db"), store.WithLog(l))
		if err != nil {
			return nil, err
		}
		w.db = db
		w.t.Cleanup(func() { _ = db.Close() })
	}
	if w.mem == nil {
		cp = w.db
	}
	p := &proc{log: l, db: w.db}
	var err error
	var node Node = w.node
	if w.wrap != nil {
		node = w.wrap(w.node)
	}
	p.r, err = Start(ctx, w.cfg, Deps{Clock: w.clock, Log: l, Store: cp, Node: node, Pacer: w.node,
		CheckpointEvery: every, Crash: crash, Pods: w.pods, Controls: w.controls, Viewer: w.monitor})
	if err != nil {
		p.kill(w.t)
		return nil, err
	}
	return p, nil
}

// kill models a power loss: whatever the log had not fsynced is gone.
func (p *proc) kill(t testing.TB) {
	t.Helper()
	ctx := context.Background()
	if err := p.log.TruncateAfter(ctx, p.log.DurableSeq()); err != nil {
		t.Fatal(err)
	}
}

func (p *proc) stop(testing.TB) {}

// run drives the world to until, restarting after every injected crash.
// crashAt reports whether the n-th crash-hook call (counted across the whole
// run) kills the engine.
func (w *world) run(until sched.Time, every int, crashAt func(n int) bool) (crashes int) {
	w.t.Helper()
	n := 0
	hook := func(string) error {
		n++
		if crashAt != nil && crashAt(n) {
			return errCrash
		}
		return nil
	}
	for {
		p, err := w.start(hook, every)
		if errors.Is(err, errCrash) {
			crashes++
			continue
		}
		if err != nil {
			w.t.Fatal(err)
		}
		err = p.r.Run(context.Background(), until)
		if errors.Is(err, errCrash) {
			p.kill(w.t)
			crashes++
			continue
		}
		if err != nil {
			w.t.Fatal(err)
		}
		snapCheck(w.t, p)
		p.stop(w.t)
		return crashes
	}
}

func snapCheck(t testing.TB, p *proc) {
	t.Helper()
	if p.log.DurableSeq() != p.log.LastSeq() {
		t.Fatalf("log not flushed: durable %d, last %d", p.log.DurableSeq(), p.log.LastSeq())
	}
}

// logBytes is the log as a file would hold it.
func (w *world) logBytes() []byte {
	w.t.Helper()
	var out []byte
	for _, r := range logRecords(w.t, w.log) {
		var err error
		if out, err = eventlog.AppendFrame(out, r); err != nil {
			w.t.Fatal(err)
		}
	}
	return out
}

// finalSnapshot replays the world's log from nothing.
func (w *world) finalSnapshot() []byte {
	w.t.Helper()
	res, err := (&eventlog.Replayer{New: w.cfg.Factory(), Log: w.log}).Replay(context.Background(), nil, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	if len(res.Diff) > 0 {
		w.t.Fatalf("replay diff: %v", res.Diff[0])
	}
	return res.State
}

// derive is the model's Derive: the owned balances implied by every block on
// the chain at least K deep, applied to genesis.
func derive(t testing.TB, cfg Config, c *fakechain.Chain) map[uint64]uint64 {
	t.Helper()
	funds, err := cfg.Genesis()
	if err != nil {
		t.Fatal(err)
	}
	type out struct {
		owner    uint64
		lovelace uint64
	}
	owner := map[string]uint64{}
	set := map[Ref]out{}
	for _, f := range funds {
		a, _ := f.Address.Bytes()
		set[Ref{Tx: TxID(blake2b256(a))}] = out{f.Actor, f.Lovelace}
		owner[string(a)] = f.Actor
	}
	ks, err := keys.New(cfg.Seed.Derive("keys"), cfg.NetworkID)
	if err != nil {
		t.Fatal(err)
	}
	// Actors born after genesis have higher IDs; a margin covers them.
	for i := uint64(1); i <= uint64(len(funds))+256; i++ {
		a, _ := ks.Wallet(keys.ActorID(i)).Address().Bytes()
		owner[string(a)] = i
	}
	blocks := c.Blocks()
	for _, b := range blocks[:max(len(blocks)-int(cfg.K), 0)] {
		raw, err := Digest(b.Cbor)
		if err != nil {
			t.Fatal(err)
		}
		var bc BlockContent
		if err := eventlog.Decode(raw, &bc); err != nil {
			t.Fatal(err)
		}
		for _, tx := range bc.Txs {
			for _, in := range tx.Inputs {
				delete(set, in)
			}
			for _, o := range tx.Outputs {
				id, ok := owner[string(o.Address)]
				if ok {
					set[Ref{Tx: tx.ID, Index: o.Index}] = out{id, o.Lovelace}
				}
			}
		}
	}
	bal := map[uint64]uint64{}
	for _, o := range set {
		bal[o.owner] += o.lovelace
	}
	return bal
}

func genesisTotal(t testing.TB, cfg Config) uint64 {
	t.Helper()
	funds, err := cfg.Genesis()
	if err != nil {
		t.Fatal(err)
	}
	var sum uint64
	for _, f := range funds {
		sum += f.Lovelace
	}
	return sum
}

// logRecords reads every record of a log.
func logRecords(t testing.TB, r eventlog.Reader) []eventlog.Record {
	t.Helper()
	var out []eventlog.Record
	if err := r.Scan(context.Background(), 1, func(rec eventlog.Record) error {
		out = append(out, rec)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// memStore is store.DB's checkpoint contract without SQLite: it refuses a
// checkpoint above the log's durable seq or whose points miss the confirmed
// point, and keeps only the last one.
type memStore struct {
	log *eventlog.MemLog
	cp  store.Checkpoint
}

func (m *memStore) SaveCheckpoint(_ context.Context, c store.Checkpoint) error {
	if c.Seq > m.log.DurableSeq() {
		return store.ErrAheadOfLog
	}
	found := false
	for _, p := range c.Points {
		found = found || samePoint(p, c.Confirmed)
	}
	if !found {
		return store.ErrPointsShort
	}
	m.cp = c
	return nil
}

func (m *memStore) Recover(context.Context) (store.Recovery, error) {
	return store.Recovery{Checkpoint: m.cp}, nil
}
