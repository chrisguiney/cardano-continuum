package httpapi

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/explorer"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// poolsTTL is how long the pool table is reused.
const poolsTTL = 3 * time.Second

var reHash64 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ExplorerOptions wires an Explorer. Kube, Tips, Names, Slots, Logger and
// Now are optional.
type ExplorerOptions struct {
	// Keep is how many of the newest blocks the index holds in full, and K
	// the security parameter it raises Keep to at least (K+1).
	Keep   int
	K      uint64
	Slots  *sched.Slots
	Limits explorer.Limits
	Kube   Kube
	Tips   Tips
	Names  Names
	Logger *slog.Logger
	Now    func() time.Time
}

// Explorer serves the chain explorer: the block index plus what only the
// run knows (pools, actors, submissions, findings). Feed it every record the
// run appends, in order, through Apply (Projection.Observe does).
//
// Lock order: the index's lock, then mu; Explorer never calls the index with
// mu held.
type Explorer struct {
	opt    ExplorerOptions
	idx    *explorer.Index
	fold   *txFold
	actors *actorDir
	log    *slog.Logger

	skipped, failed atomic.Uint64

	mu     sync.Mutex // protects table, tableAt
	table  map[string]explorer.PoolRef
	tableT time.Time
}

// NewExplorer returns an Explorer over an empty index.
func NewExplorer(o ExplorerOptions) *Explorer {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	e := &Explorer{opt: o, fold: newTxFold(), actors: newActorDir(o.Names, o.Now), log: o.Logger.With("component", "explorer"), table: map[string]explorer.PoolRef{}}
	e.idx = explorer.New(explorer.Config{
		Keep: o.Keep, K: o.K, Slots: o.Slots, Limits: o.Limits, Pool: e.resolve, Known: e.known,
	})
	return e
}

// Apply folds one log record: a block or rollback into the index, a
// submission, verdict or finding into the transaction context.
func (e *Explorer) Apply(r eventlog.Record) {
	e.fold.apply(r)
	switch {
	case r.Class == eventlog.Input && r.Kind == eventlog.KindBlockApplied:
		e.block(r)
	case r.Class == eventlog.Input && r.Kind == eventlog.KindRolledBack:
		e.rollback(r)
	}
}

func (e *Explorer) block(r eventlog.Record) {
	var b eventlog.BlockApplied
	if err := eventlog.Decode(r.Body, &b); err != nil || len(b.Content) == 0 {
		e.skipped.Add(1)
		return
	}
	var c engine.BlockContent
	if err := eventlog.Decode(b.Content, &c); err != nil || len(c.Raw) == 0 {
		e.skipped.Add(1)
		return
	}
	if err := e.idx.Add(c.Raw); err != nil {
		e.failed.Add(1)
		e.log.Warn("index block", "seq", r.Seq, "slot", b.Point.Slot, "err", err)
	}
}

func (e *Explorer) rollback(r eventlog.Record) {
	var b eventlog.RolledBack
	if err := eventlog.Decode(r.Body, &b); err != nil {
		e.failed.Add(1)
		return
	}
	if len(b.To.Hash) == 0 {
		return
	}
	if err := e.idx.Rollback(b.To.Slot, hex.EncodeToString(b.To.Hash)); err != nil {
		e.failed.Add(1)
		e.log.Warn("index rollback", "seq", r.Seq, "slot", b.To.Slot, "err", err)
	}
}

// Status is what the index holds.
func (e *Explorer) Status() ExplorerStatus {
	n, h := e.idx.Tip()
	return ExplorerStatus{Blocks: n, FullBlocks: e.idx.FullBlocks(), Tip: h, Skipped: e.skipped.Load(), Failed: e.failed.Load()}
}

// Blocks lists blocks newest first.
func (e *Explorer) Blocks(ctx context.Context, q explorer.BlockQuery) (explorer.BlockPage, error) {
	if err := e.refresh(ctx); err != nil {
		return explorer.BlockPage{}, err
	}
	return e.idx.Blocks(q), nil
}

// Block returns a block with where every pool's chain stands against it. A
// hash the index does not hold but a pool's tip does is a stub: the other
// side of a fork.
func (e *Explorer) Block(ctx context.Context, key string) (BlockDetail, error) {
	if err := e.refresh(ctx); err != nil {
		return BlockDetail{}, err
	}
	tips := e.tips()
	v, err := e.idx.Block(key)
	switch {
	case errors.Is(err, explorer.ErrNotFound) && reHash64.MatchString(key):
		stub, ok := stubOf(strings.ToLower(key), e.opt.Tips)
		if !ok {
			return BlockDetail{}, exErr(err)
		}
		return withPlacement(stub, tips), nil
	case err != nil:
		return BlockDetail{}, exErr(err)
	}
	return withPlacement(BlockDetail{BlockView: v}, tips), nil
}

// Tx returns a transaction with its continuum context.
func (e *Explorer) Tx(ctx context.Context, hash string) (TxDetail, error) {
	v, err := e.idx.Tx(hash)
	if err != nil {
		return TxDetail{}, exErr(err)
	}
	d := TxDetail{TxView: v}
	id, err := hex.DecodeString(v.Hash)
	if err != nil || len(id) != 32 {
		return d, nil
	}
	sub, ver, ok := e.fold.of([32]byte(id))
	fs := e.fold.findingsFor([32]byte(id))
	if !ok && len(fs) == 0 {
		return d, nil
	}
	c := &TxContext{Findings: fs}
	if ok {
		c.Intent, c.Kind, c.Amount, c.Attack, c.Verdict, c.Reason = sub.intent, sub.kind, sub.amount, sub.attack, ver.name, ver.reason
		if c.Actor, err = e.actors.ofID(ctx, sub.actor); err != nil {
			return TxDetail{}, err
		}
		if c.Actor == nil {
			c.Actor = &ActorRef{ID: sub.actor}
		}
	}
	d.Continuum = c
	return d, nil
}

// TxCbor returns the raw transaction.
func (e *Explorer) TxCbor(hash string) ([]byte, error) {
	v, err := e.idx.Tx(hash)
	if err != nil {
		return nil, exErr(err)
	}
	if v.Evicted || v.CborHex == "" {
		return nil, fmt.Errorf("%w: the transaction's block is no longer held in full", ErrNotFound)
	}
	b, err := hex.DecodeString(v.CborHex)
	if err != nil {
		return nil, fmt.Errorf("decode transaction cbor: %w", err)
	}
	return b, nil
}

// Address returns an address with the actor that owns it.
func (e *Explorer) Address(ctx context.Context, addr string, p explorer.Page) (AddressDetail, error) {
	v, err := e.idx.Address(addr, p)
	if err != nil {
		return AddressDetail{}, exErr(err)
	}
	d := AddressDetail{AddressView: v}
	if d.Actor, err = e.actors.ofAddress(ctx, v.Address); err != nil {
		return AddressDetail{}, err
	}
	if v.Stake != "" {
		if d.StakeActor, err = e.actors.ofAddress(ctx, v.Stake); err != nil {
			return AddressDetail{}, err
		}
	}
	return d, nil
}

// Epoch summarises an epoch.
func (e *Explorer) Epoch(ctx context.Context, n uint64) (explorer.EpochView, error) {
	if err := e.refresh(ctx); err != nil {
		return explorer.EpochView{}, err
	}
	v, err := e.idx.Epoch(n)
	return v, exErr(err)
}

// Search routes a key to the view that shows it.
func (e *Explorer) Search(ctx context.Context, q string) (SearchResult, error) {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 200 {
		return SearchResult{}, badf("q must be 1..200 characters")
	}
	if err := e.refresh(ctx); err != nil {
		return SearchResult{}, err
	}
	switch explorer.Classify(q) {
	case explorer.KindActor:
		id := strings.TrimLeft(strings.TrimPrefix(q, "actor:"), "@")
		return SearchResult{Kind: explorer.KindActor, ID: id, Route: "/actors/" + id}, nil
	case explorer.KindPool:
		if id, ok := e.poolID(q); ok {
			return SearchResult{Kind: explorer.KindPool, ID: id, Route: "/explorer/pools/" + id}, nil
		}
	default:
		if h, ok := e.idx.Find(q); ok {
			return SearchResult{Kind: h.Kind, ID: h.ID, Route: route(h)}, nil
		}
	}
	if id, ok := e.poolID(q); ok {
		return SearchResult{Kind: explorer.KindPool, ID: id, Route: "/explorer/pools/" + id}, nil
	}
	if reHash64.MatchString(q) {
		if _, ok := stubOf(strings.ToLower(q), e.opt.Tips); ok {
			return SearchResult{Kind: explorer.KindBlock, ID: strings.ToLower(q), Route: "/explorer/blocks/" + strings.ToLower(q)}, nil
		}
	}
	return SearchResult{}, fmt.Errorf("%w: nothing matches %q", ErrNotFound, q)
}

func route(h explorer.Hit) string {
	switch h.Kind {
	case explorer.KindBlock:
		return "/explorer/blocks/" + h.ID
	case explorer.KindTx:
		return "/explorer/txs/" + h.ID
	}
	return "/explorer/addresses/" + h.ID
}

// exErr maps the index's errors to the API's.
func exErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, explorer.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrNotFound, strings.TrimPrefix(err.Error(), "explorer: "))
	case errors.Is(err, explorer.ErrBadKey):
		return badf("%s", strings.TrimPrefix(err.Error(), "explorer: "))
	}
	return err
}

// refresh rereads the pool table when it is older than poolsTTL.
func (e *Explorer) refresh(ctx context.Context) error {
	e.mu.Lock()
	fresh := e.opt.Now().Sub(e.tableT) < poolsTTL
	e.mu.Unlock()
	if fresh || e.opt.Kube == nil && e.opt.Tips == nil {
		return nil
	}
	table := map[string]explorer.PoolRef{}
	byName := map[string]string{}
	if e.opt.Kube != nil {
		ps, err := e.opt.Kube.Pools(ctx)
		if err != nil {
			return fmt.Errorf("list pools: %w", err)
		}
		for _, p := range ps {
			ref := explorer.PoolRef{Name: p.Name, Impl: string(p.Spec.Implementation)}
			byName[p.Name] = ref.Impl
			if isPoolHex(p.Spec.PoolID) {
				table[strings.ToLower(p.Spec.PoolID)] = ref
			}
		}
	}
	if e.opt.Tips != nil {
		for name, marks := range e.opt.Tips.Tips() {
			for _, m := range marks {
				if m.Issuer != ([28]byte{}) {
					table[hex.EncodeToString(m.Issuer[:])] = explorer.PoolRef{Name: name, Impl: byName[name]}
				}
			}
		}
	}
	e.mu.Lock()
	e.table, e.tableT = table, e.opt.Now()
	e.mu.Unlock()
	return nil
}

func isPoolHex(s string) bool { return len(s) == 56 && explorer.Classify(s) == explorer.KindPool }

func (e *Explorer) resolve(id string) (explorer.PoolRef, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.table[id]
	return p, ok
}

func (e *Explorer) known() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make([]string, 0, len(e.table))
	for id := range e.table {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// poolID finds the pool id (hex) a search key names: a hex id, or a
// continuum pool's name.
func (e *Explorer) poolID(q string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.table[strings.ToLower(q)]; ok && p.Name != "" {
		return strings.ToLower(q), true
	}
	for id, p := range e.table {
		if p.Name == q {
			return id, true
		}
	}
	return "", false
}

// tips is each pool's followed chain as heights and hashes.
func (e *Explorer) tips() map[string][]explorer.Tip {
	out := map[string][]explorer.Tip{}
	if e.opt.Tips == nil {
		return out
	}
	for name, marks := range e.opt.Tips.Tips() {
		for _, m := range marks {
			out[name] = append(out[name], explorer.Tip{Height: m.Height, Hash: hex.EncodeToString(m.Hash[:])})
		}
	}
	return out
}

func withPlacement(b BlockDetail, tips map[string][]explorer.Tip) BlockDetail {
	b.Pools = explorer.Placement(b.Height, b.Hash, tips)
	for _, p := range b.Pools {
		b.Fork = b.Fork || p.State == "fork"
	}
	return b
}

// stubOf describes a block only a pool's tip knows.
func stubOf(hash string, tips Tips) (BlockDetail, bool) {
	if tips == nil {
		return BlockDetail{}, false
	}
	for _, marks := range tips.Tips() {
		for _, m := range marks {
			if hex.EncodeToString(m.Hash[:]) == hash {
				d := BlockDetail{Stub: true}
				d.Hash, d.Height, d.Slot = hash, m.Height, m.Slot
				if m.Issuer != ([28]byte{}) {
					d.Issuer = hex.EncodeToString(m.Issuer[:])
				}
				d.Txs = []explorer.TxRow{}
				return d, true
			}
		}
	}
	return BlockDetail{}, false
}
