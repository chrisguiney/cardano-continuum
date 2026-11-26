package forger

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Config is what a Node runs with.
type Config struct {
	Params Params
	// Creds are the pool's keys. Without them the node follows and serves
	// the chain and never forges.
	Creds *Credentials
	// Schedule is the scenario's misbehaviours; the zero value is honest.
	Schedule Schedule
	// Listener accepts node-to-node peers. Run closes it.
	Listener net.Listener
	// Upstreams are the host:port of the nodes whose chain is followed,
	// tried in order. With none the forger extends only its own chain.
	Upstreams []string
	// EvidenceDir, when set, receives the CBOR of every forged block.
	EvidenceDir string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Node is a block producer that follows a peer's chain, forges on it with
// its pool's keys, and serves each peer the chain its Schedule chooses.
type Node struct {
	cfg     Config
	log     *slog.Logger
	planner *Planner
	now     func() time.Time
	st      *shared
	srv     *server
	fol     *follower
}

// New checks cfg and builds a Node.
func New(cfg Config, log *slog.Logger) (*Node, error) {
	if cfg.Listener == nil {
		return nil, errors.New("forger needs a listener")
	}
	if log == nil {
		return nil, errors.New("forger needs a logger")
	}
	if err := cfg.Params.check(); err != nil {
		return nil, fmt.Errorf("forger params: %w", err)
	}
	n := &Node{
		cfg: cfg, log: log.With("component", "forger"), now: cfg.Now,
		st: &shared{
			changed:  make(chan struct{}, 1),
			sessions: map[string]*session{},
			rogue:    map[[32]byte]Kind{},
			synced:   len(cfg.Upstreams) == 0,
		},
	}
	if n.now == nil {
		n.now = time.Now
	}
	var issuer [32]byte
	if cfg.Creds != nil {
		copy(issuer[:], cfg.Creds.ColdVKey)
		var err error
		if n.planner, err = NewPlanner(cfg.Params, cfg.Creds, cfg.Schedule); err != nil {
			return nil, fmt.Errorf("forger schedule: %w", err)
		}
	}
	n.st.chain = NewChain(cfg.Params, issuer)
	n.srv = &server{log: n.log, magic: cfg.Params.Magic, listener: cfg.Listener, st: n.st}
	n.fol = &follower{log: n.log, magic: cfg.Params.Magic, upstreams: cfg.Upstreams, st: n.st}
	return n, nil
}

// Run serves peers, follows the upstream and forges until ctx is cancelled
// or a part fails. It returns after every goroutine it started has exited
// and every connection is closed.
func (n *Node) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	parts := []func(context.Context) error{n.srv.accept, n.srv.answer}
	if len(n.cfg.Upstreams) > 0 {
		parts = append(parts, n.fol.follow)
	}
	if n.planner != nil {
		parts = append(parts, n.forge)
	}
	errs := make(chan error, len(parts))
	for _, part := range parts {
		go func() { errs <- part(ctx) }()
	}
	var first error
	for range parts {
		if err := <-errs; err != nil && first == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			first = err
		}
		cancel()
	}
	return first
}

// forge plans and forges at the start of every slot.
func (n *Node) forge(ctx context.Context) error {
	p := n.cfg.Params
	slot, started := p.SlotAt(n.now())
	if started {
		// The slot in progress began before the forger was ready for it.
		slot++
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for ; ; slot++ {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(p.SlotStart(slot).Sub(n.now()))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		if err := n.onSlot(slot); err != nil {
			return fmt.Errorf("forge at slot %d: %w", slot, err)
		}
	}
}

// onSlot forges whatever the schedule plans for slot. It forges nothing
// where shared.plan reports none.
func (n *Node) onSlot(slot uint64) error {
	st, ok := n.st.plan(n.cfg.Params, slot)
	if !ok {
		return nil
	}
	var err error
	if st.Leader, err = Leader(n.cfg.Params, n.cfg.Creds, st.Nonce, slot); err != nil {
		return err
	}
	forges, err := n.planner.Plan(st)
	if err != nil {
		return err
	}
	for _, f := range forges {
		b, err := Assemble(n.cfg.Params, n.cfg.Creds, st.Nonce, f)
		if err != nil {
			return fmt.Errorf("assemble %s block: %w", f.Kind, err)
		}
		path, err := n.keep(b, f)
		if err != nil {
			return err
		}
		n.st.add(b, f)
		n.log.Info("forged", decisionAttrs(n.cfg.Schedule, st, f, b, path)...)
	}
	if len(forges) > 0 {
		n.st.wake()
	}
	return nil
}

// keep writes a forged block's CBOR to the evidence directory and returns
// its path, or "" without one.
func (n *Node) keep(b Block, f Forge) (string, error) {
	if n.cfg.EvidenceDir == "" {
		return "", nil
	}
	name := fmt.Sprintf("%d-%s-%s.block", b.Slot, f.Kind, hex.EncodeToString(b.Hash[:8]))
	path := filepath.Join(n.cfg.EvidenceDir, name)
	if err := os.WriteFile(path, b.CBOR, 0o644); err != nil {
		return "", fmt.Errorf("keep block evidence: %w", err)
	}
	return path, nil
}

// decisionAttrs is the log record of one forged block: enough to reproduce
// the decision (seed, slot, kind) and to recognise the block on a chain.
func decisionAttrs(s Schedule, st State, f Forge, b Block, evidence string) []any {
	peers := make([]string, len(f.SendTo))
	for i, p := range f.SendTo {
		peers[i] = string(p)
	}
	return []any{
		"kind", string(f.Kind), "slot", st.Slot, "block_slot", b.Slot, "block_number", b.Number,
		"hash", hex.EncodeToString(b.Hash[:]), "prev", hex.EncodeToString(b.Prev[:]),
		"expect", string(f.Expect), "rule", f.Rule, "leader", st.Leader,
		"cert", certFile(f.Cert), "counter", b.Counter, "chain_counter", st.Counter,
		"send_to", peers, "body_bytes", b.BodySize, "evidence", evidence,
		"seed", hex.EncodeToString(s.Seed[:]),
	}
}
