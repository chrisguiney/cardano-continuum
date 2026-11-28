package httpapi

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/explorer"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

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
	// Params are the protocol parameters in force, which a parameter-change
	// action's current values are read from; nil leaves them unknown. The
	// run never enacts an update, so its genesis parameters stay current.
	Params *conway.ConwayProtocolParameters
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

	pools *poolTable
}

// NewExplorer returns an Explorer over an empty index.
func NewExplorer(o ExplorerOptions) *Explorer {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	e := &Explorer{opt: o, fold: newTxFold(), actors: newActorDir(o.Names, o.Now), log: o.Logger.With("component", "explorer")}
	e.pools = newPoolTable(o.Kube, o.Tips, o.Now, func(hash string) (string, bool) { return issuerIn(e.idx, hash) })
	e.idx = explorer.New(explorer.Config{
		Keep: o.Keep, K: o.K, Slots: o.Slots, Limits: o.Limits, Pool: e.pools.resolve, Known: e.pools.known,
	})
	return e
}

// issuerIn is the issuer (hex pool id) of a block idx holds.
func issuerIn(idx *explorer.Index, hash string) (string, bool) {
	v, err := idx.Block(hash)
	if err != nil || v.Issuer == "" {
		return "", false
	}
	return v.Issuer, true
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
	if err := e.pools.refresh(ctx); err != nil {
		return explorer.BlockPage{}, err
	}
	return e.idx.Blocks(q), nil
}

// Block returns a block with where every pool's chain stands against it. A
// hash the index does not hold but a pool's tip does is a stub: the other
// side of a fork.
func (e *Explorer) Block(ctx context.Context, key string) (BlockDetail, error) {
	if err := e.pools.refresh(ctx); err != nil {
		return BlockDetail{}, err
	}
	tips := e.pools.tips()
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
		c.Validator, c.Scripts = sub.validator, sub.scripts
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
		return nil, fmt.Errorf("%w: the transaction's block is no longer held in full", view.ErrNotFound)
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
	if err := e.pools.refresh(ctx); err != nil {
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
	if err := e.pools.refresh(ctx); err != nil {
		return SearchResult{}, err
	}
	switch explorer.Classify(q) {
	case explorer.KindActor:
		id := strings.TrimLeft(strings.TrimPrefix(q, "actor:"), "@")
		return SearchResult{Kind: explorer.KindActor, ID: id, Route: "/actors/" + id}, nil
	case explorer.KindPool:
		if id, ok := stakePoolOf(e, q); ok {
			return SearchResult{Kind: explorer.KindPool, ID: id, Route: "/pools/" + id}, nil
		}
	default:
		if h, ok := e.idx.Find(q); ok {
			return SearchResult{Kind: h.Kind, ID: h.ID, Route: route(h)}, nil
		}
	}
	if e.pools.node(q) {
		return SearchResult{Kind: explorer.KindNode, ID: q, Route: "/nodes/" + url.PathEscape(q)}, nil
	}
	if reHash64.MatchString(q) {
		if _, ok := stubOf(strings.ToLower(q), e.opt.Tips); ok {
			return SearchResult{Kind: explorer.KindBlock, ID: strings.ToLower(q), Route: "/explorer/blocks/" + strings.ToLower(q)}, nil
		}
	}
	return SearchResult{}, fmt.Errorf("%w: nothing matches %q", view.ErrNotFound, q)
}

// stakePoolOf is the pool id (hex) q names, hex or bech32, when the chain
// or a node knows the pool.
func stakePoolOf(e *Explorer, q string) (string, bool) {
	id, ok := view.PoolHex(q)
	if !ok {
		if id, ok = poolHexKey(q); !ok {
			return "", false
		}
	}
	if _, ok := e.pools.poolID(id); ok {
		return id, true
	}
	for _, p := range e.idx.Pools() {
		if p.ID == id {
			return id, true
		}
	}
	return "", false
}

// poolHexKey is q as a lowercase 56-digit hex pool id, if it is one.
func poolHexKey(q string) (string, bool) {
	q = strings.ToLower(q)
	return q, rePoolHex.MatchString(q)
}

// withKeys returns v with each forging node the source could not resolve
// given the issuer of its last own block, if idx holds that block.
func withKeys(idx *explorer.Index, v view.NodesView) view.NodesView {
	v.Nodes = slices.Clone(v.Nodes)
	for i, n := range v.Nodes {
		if n.StakePool != "" || n.Tier != string(v1alpha1.TierForging) || n.LastBlock == nil {
			continue
		}
		if id, ok := issuerIn(idx, n.LastBlock.Hash); ok {
			v.Nodes[i].StakePool = id
		}
	}
	return v
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
		return fmt.Errorf("%w: %s", view.ErrNotFound, strings.TrimPrefix(err.Error(), "explorer: "))
	case errors.Is(err, explorer.ErrBadKey):
		return badf("%s", strings.TrimPrefix(err.Error(), "explorer: "))
	}
	return err
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
	m, ok := markOf(hash, tips)
	if !ok {
		return BlockDetail{}, false
	}
	d := BlockDetail{Stub: true}
	d.Hash, d.Height, d.Slot = hash, m.Height, m.Slot
	if m.Issuer != ([28]byte{}) {
		d.Issuer = hex.EncodeToString(m.Issuer[:])
	}
	d.Txs = []explorer.TxRow{}
	return d, true
}

// markOf finds the block a pool's tip reports under hash.
func markOf(hash string, tips Tips) (chain.Mark, bool) {
	if tips == nil {
		return chain.Mark{}, false
	}
	for _, marks := range tips.Tips() {
		for _, m := range marks {
			if hex.EncodeToString(m.Hash[:]) == hash {
				return m, true
			}
		}
	}
	return chain.Mark{}, false
}
