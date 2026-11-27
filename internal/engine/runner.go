package engine

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	// Aliased: the package tests declare a recovery type of their own.
	restart "github.com/chrisguiney/cardano-continuum/internal/engine/internal/recovery"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/send"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Node is the chain the loop submits to and follows.
type Node interface {
	// Submit returns nil when the node accepted tx. A refusal must be an
	// error internal/submit recognises (submit.RejectedError, the fake
	// chain's or local-tx-submission's rejection); any other error is
	// taken as unreached, which releases nothing.
	Submit(ctx context.Context, tx []byte) error
	// HasTx reports whether the mempool already holds the transaction, so a
	// submission repeated after a crash is not mistaken for a conflict.
	HasTx(id TxID) bool
	// Intersect is chain-sync's FindIntersect: points newest first; the
	// answer is the first one on the chain, possibly older than the first.
	Intersect(points []eventlog.ChainPoint) (Follower, eventlog.ChainPoint, error)
}

// Dropper is a Node that reports transactions leaving its mempool without
// a block (the fake chain's evictions). Live has no mempool monitor, so it
// is optional.
type Dropper interface {
	// Dropped returns the transactions dropped since the last call.
	Dropped() []TxID
}

// Pacer drives a chain that has no clock of its own (the fake chain).
type Pacer interface {
	Advance(t sched.Time) error
	// NextChange is the next time after now at which the chain may change.
	NextChange(now sched.Time) sched.Time
}

// PodSource reports the pool pods' observed health: the pools whose pod
// started or stopped running since the last call. A live run reads the
// operator's ContinuumPool status; tests script it.
type PodSource interface {
	Changes(ctx context.Context, now sched.Time) ([]PoolStatus, error)
}

// ControlSource hands the loop the controls submitted since the last call,
// each the CBOR body of a Control input (httpapi.Control). A live run's
// HTTP API queues them; the loop logs and applies them in order.
type ControlSource interface {
	Controls(ctx context.Context) ([][]byte, error)
}

// Log is the event log as the loop uses it.
type Log interface {
	eventlog.Writer
	eventlog.Reader
	LastSeq() uint64
	DurableSeq() uint64
}

// Checkpointer is the store as the loop uses it.
type Checkpointer = restart.Store

// Crash stages: the points at which Deps.Crash may stop the loop.
const (
	StageFed          = restart.StageFed
	StageSubmitted    = restart.StageSubmitted
	StageSynced       = restart.StageSynced
	StageCheckpointed = restart.StageCheckpointed
)

// Deps are the loop's collaborators.
type Deps struct {
	Clock sched.Clock
	Log   Log
	// Store is optional; without it recovery replays the whole log.
	Store Checkpointer
	Node  Node
	// Pacer is optional; the fake chain needs one.
	Pacer Pacer
	// Pods is optional: without it no pod is ever observed down.
	Pods PodSource
	// Controls is optional: the run's steering.
	Controls ControlSource
	// Pools is optional: it applies the engine's pool plan (go-dark and
	// upgrade images) to the pools a live run's operator renders.
	Pools PoolWriter
	// CheckpointEvery is the number of inputs between checkpoints; zero
	// checkpoints only when Run returns.
	CheckpointEvery int
	// Viewer is optional: it answers the view monitor, which compares the
	// engine's settled balances with the node's every ViewEvery of
	// simulated time (zero: at every new settled point).
	Viewer    Viewer
	ViewEvery sched.Time
	// Tips is optional: it supplies the pools' followed chains to the fork
	// monitor, asked every ViewEvery of simulated time.
	Tips TipSource
	// Findings, when set, is the set the engine logs its findings into,
	// so an API can serve it while the loop runs. It must be empty: the
	// runner loads what the log holds.
	Findings *oracle.Set
	// Crash, when set, is called at every stage; a non-nil error stops the
	// loop on the spot, as a kill would. Tests use it for crash injection.
	Crash  func(stage string) error
	Logger *slog.Logger
}

// ErrDiverged: replaying the log on restart produced different decisions,
// so the log was written by a different engine or the engine is not
// deterministic.
var ErrDiverged = restart.ErrDiverged

// Stats count what the loop did in this process.
type Stats struct {
	Inputs, Ticks, Blocks, Rollbacks, Submissions, Rejections, Unreached uint64
}

// Runner is the I/O shell around Engine: one goroutine that logs each input
// before the engine sees it, performs the decided submissions, and
// checkpoints.
type Runner struct {
	cfg    Config
	d      Deps
	eng    *Engine
	sess   eventlog.Session
	follow Follower
	send   *send.Sender
	ckpt   *restart.Saver
	stats  Stats
	// plan is the pool plan last synced.
	plan   []PoolPlan
	synced bool
	// viewed and viewAt are the settled point and time of the last view
	// query.
	viewed  eventlog.ChainPoint
	viewAt  sched.Time
	queried bool
	tipAt   sched.Time
	tipped  bool
	log     *slog.Logger
}

// Identifier is a Node that knows which implementation and build it is,
// for the oracle's findings.
type Identifier interface {
	Identity() oracle.Node
}

// Start opens a run: a fresh one on an empty log, otherwise a recovery from
// the checkpoint plus the log tail. It then resumes submissions a crash
// interrupted and asks the node for an intersection, logging a rollback when
// the answer is below the engine's tip, before any new block.
func Start(ctx context.Context, cfg Config, d Deps) (*Runner, error) {
	hdr, err := cfg.Header()
	if err != nil {
		return nil, err
	}
	r := &Runner{cfg: cfg, d: d, log: d.Logger}
	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}
	r.log = r.log.With("component", "engine")
	snap, covers, err := restart.Open(ctx, d.Log, d.Store, hdr)
	if err != nil {
		return nil, err
	}
	if err := restart.Replay(ctx, d.Log, snap, covers, r.factory(ctx, covers)); err != nil {
		return nil, err
	}
	r.ckpt = restart.NewSaver(d.Log, d.Store, d.CheckpointEvery, d.Crash)
	r.sess = eventlog.Session{Eng: r.eng, Log: d.Log}
	var who *oracle.Node
	if id, ok := d.Node.(Identifier); ok {
		n := id.Identity()
		who = &n
	}
	r.send = send.New(d.Node, d.Log, who, r.eng.pay.InFlight(r.eng.clk.Now()))
	r.log.Info("engine started", "covers", covers, "log_seq", d.Log.LastSeq(), "actors", r.eng.Summary().Actors)
	if err := r.effects(ctx); err != nil {
		return nil, err
	}
	if err := r.intersect(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// factory builds the engine replay starts from and keeps it as r.eng.
func (r *Runner) factory(ctx context.Context, covers uint64) eventlog.Factory {
	factory := r.cfg.Factory()
	return func(s []byte) (eventlog.Engine, error) {
		e, err := factory(s)
		if err != nil {
			return e, err
		}
		r.eng = e.(*Engine)
		if r.d.Findings != nil {
			r.eng.jd.Use(r.d.Findings)
		}
		// The checkpoint's findings are in the log, not the snapshot; the
		// tail's are regenerated by the replay that follows.
		return e, restart.LoadFindings(ctx, r.d.Log, covers, r.eng.jd.Set())
	}
}

func (r *Runner) intersect(ctx context.Context) error {
	pts := r.eng.win.Points()
	slices.Reverse(pts)
	f, at, err := r.d.Node.Intersect(pts)
	if err != nil {
		return fmt.Errorf("engine: find intersect: %w", err)
	}
	r.follow = f
	wrote, err := r.sess.Intersect(ctx, r.d.Clock.Now(), r.eng.Summary().Tip, at)
	if err != nil {
		return err
	}
	if wrote {
		r.stats.Inputs++
		r.stats.Rollbacks++
		r.log.Info("intersection below the stored tip", "slot", at.Slot)
	}
	return nil
}

// step runs one logged input, then the post-input hooks.
func (r *Runner) step(ctx context.Context, feed func() error) error {
	if err := feed(); err != nil {
		return err
	}
	r.stats.Inputs++
	due, err := r.ckpt.Fed()
	if err != nil {
		return err
	}
	if r.eng.fl.Stalled() {
		return ErrStalled
	}
	if due {
		return r.Checkpoint(ctx)
	}
	return nil
}

// effects submits every payment the engine is waiting on, oldest first, and
// logs each answer as a SubmitResult input. The Submitted decision, which
// carries the reservation, is synced before its transaction leaves.
func (r *Runner) effects(ctx context.Context) error {
	for {
		inf := r.eng.pay.InFlight(r.eng.clk.Now())
		if len(inf) == 0 {
			return nil
		}
		s := inf[0]
		res, err := r.send.Send(ctx, s)
		if err != nil {
			return err
		}
		r.stats.Submissions++
		if err := r.ckpt.Crash(StageSubmitted); err != nil {
			return err
		}
		body, err := eventlog.Encode(res)
		if err != nil {
			return err
		}
		rec := eventlog.Record{At: r.eng.Now(), Kind: eventlog.KindSubmitResult, Actor: eventlog.ActorID(s.Actor), Body: body}
		if err := r.step(ctx, func() error { return r.sess.Feed(ctx, rec) }); err != nil {
			return err
		}
	}
}

// drain feeds every chain event the follower has.
func (r *Runner) drain(ctx context.Context) error {
	for {
		ev, ok, err := r.follow.Next()
		if err != nil {
			return fmt.Errorf("engine: follow: %w", err)
		}
		if !ok {
			return nil
		}
		var rec eventlog.Record
		if ev.Rollback {
			r.stats.Rollbacks++
			body, err := eventlog.Encode(eventlog.RolledBack{To: ev.Point})
			if err != nil {
				return err
			}
			rec = eventlog.Record{At: r.d.Clock.Now(), Kind: eventlog.KindRolledBack, Body: body}
		} else {
			r.stats.Blocks++
			body, err := eventlog.Encode(eventlog.BlockApplied{Point: ev.Point, Content: ev.Content})
			if err != nil {
				return err
			}
			rec = eventlog.Record{At: r.d.Clock.Now(), Kind: eventlog.KindBlockApplied, Body: body}
		}
		if err := r.step(ctx, func() error { return r.sess.Feed(ctx, rec) }); err != nil {
			return err
		}
	}
}

// pods feeds every pod health change as a PoolStatus input.
func (r *Runner) pods(ctx context.Context) error {
	if r.d.Pods == nil {
		return nil
	}
	now := r.d.Clock.Now()
	ss, err := r.d.Pods.Changes(ctx, now)
	if err != nil {
		return fmt.Errorf("engine: pod status: %w", err)
	}
	for _, s := range ss {
		body, err := eventlog.Encode(s)
		if err != nil {
			return err
		}
		rec := eventlog.Record{At: now, Kind: eventlog.KindPoolStatus, Actor: eventlog.ActorID(s.Pool), Body: body}
		if err := r.step(ctx, func() error { return r.sess.Feed(ctx, rec) }); err != nil {
			return err
		}
		if err := r.effects(ctx); err != nil {
			return err
		}
	}
	return nil
}

// drops feeds a drop notice for every dropped transaction that is one of
// the engine's attempts.
func (r *Runner) drops(ctx context.Context) error {
	d, ok := r.d.Node.(Dropper)
	if !ok {
		return nil
	}
	for _, tx := range d.Dropped() {
		id, ok := r.eng.pay.Of(tx)
		if !ok {
			continue
		}
		body, err := eventlog.Encode(SubmitResult{Intent: id, Verdict: VerdictDropped, Tx: &tx})
		if err != nil {
			return err
		}
		rec := eventlog.Record{At: r.eng.Now(), Kind: eventlog.KindSubmitResult, Body: body}
		if err := r.step(ctx, func() error { return r.sess.Feed(ctx, rec) }); err != nil {
			return err
		}
	}
	return r.effects(ctx)
}

// PoolWriter applies the pool plan. Sync is idempotent: the loop calls it
// with the whole plan whenever the plan changed, and once after starting.
type PoolWriter interface {
	Sync(ctx context.Context, plan []PoolPlan) error
}

// controls logs and applies every submitted control.
func (r *Runner) controls(ctx context.Context) error {
	if r.d.Controls == nil {
		return nil
	}
	bodies, err := r.d.Controls.Controls(ctx)
	if err != nil {
		return fmt.Errorf("engine: controls: %w", err)
	}
	for _, b := range bodies {
		rec := eventlog.Record{At: r.eng.Now(), Kind: eventlog.KindControl, Body: b}
		if err := r.step(ctx, func() error { return r.sess.Feed(ctx, rec) }); err != nil {
			return err
		}
		if err := r.effects(ctx); err != nil {
			return err
		}
	}
	return nil
}

// syncPools hands the pool plan to the writer when it changed.
func (r *Runner) syncPools(ctx context.Context) error {
	if r.d.Pools == nil {
		return nil
	}
	plan := r.eng.st.Pools()
	if r.synced && slices.Equal(plan, r.plan) {
		return nil
	}
	if err := r.d.Pools.Sync(ctx, plan); err != nil {
		return fmt.Errorf("engine: sync pools: %w", err)
	}
	r.plan, r.synced = plan, true
	return nil
}

// pollEvery bounds a wait when nothing tells the loop when to wake.
const pollEvery sched.Time = 1000

// Run drives the loop until simulated time passes until or ctx ends. It
// returns after flushing the log and checkpointing, so a clean stop leaves
// nothing for recovery to replay; it returns ctx.Err() when cancelled.
func (r *Runner) Run(ctx context.Context, until sched.Time) error {
	for ctx.Err() == nil {
		if err := r.drain(ctx); err != nil {
			return err
		}
		// A block can start the engine's recovery moves.
		if err := r.effects(ctx); err != nil {
			return err
		}
		if err := r.view(ctx); err != nil {
			return err
		}
		if err := r.tips(ctx); err != nil {
			return err
		}
		if err := r.pods(ctx); err != nil {
			return err
		}
		if err := r.drops(ctx); err != nil {
			return err
		}
		if err := r.controls(ctx); err != nil {
			return err
		}
		if err := r.syncPools(ctx); err != nil {
			return err
		}
		now := r.d.Clock.Now()
		if t, ok := r.eng.NextEvent(); ok && t <= now {
			r.stats.Ticks++
			if err := r.step(ctx, func() error { return r.sess.Tick(ctx, now) }); err != nil {
				return err
			}
			if err := r.effects(ctx); err != nil {
				return err
			}
			continue
		}
		next := now + pollEvery
		if r.d.Pacer != nil {
			next = r.d.Pacer.NextChange(now)
		}
		if t, ok := r.eng.NextEvent(); ok {
			next = min(next, t)
		}
		if next > until {
			return r.flush(ctx)
		}
		if err := r.d.Clock.Wait(ctx, next); err != nil {
			break
		}
		if r.d.Pacer != nil {
			if err := r.d.Pacer.Advance(r.d.Clock.Now()); err != nil {
				return fmt.Errorf("engine: advance chain: %w", err)
			}
		}
	}
	if err := r.flush(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	return ctx.Err()
}

func (r *Runner) flush(ctx context.Context) error {
	return r.ckpt.Flush(ctx, r.state)
}

// Checkpoint flushes the log and then saves the engine state as covering
// every entry, in that order: a checkpoint never reflects an input the
// durable log lacks.
func (r *Runner) Checkpoint(ctx context.Context) error {
	return r.ckpt.Save(ctx, r.state)
}

// state is the checkpoint of the engine covering the log up to seq.
func (r *Runner) state(seq uint64) (store.Checkpoint, error) {
	snap, err := r.eng.Snapshot()
	if err != nil {
		return store.Checkpoint{}, err
	}
	res, err := r.eng.pay.Blob()
	if err != nil {
		return store.Checkpoint{}, err
	}
	return store.Checkpoint{
		Seq: seq, Points: r.eng.win.Points(), Confirmed: r.eng.win.Confirmed(),
		Actors: snap, ActorRows: actorRows(r.eng), Reservations: res,
	}, nil
}

// Engine returns the engine the loop drives.
func (r *Runner) Engine() *Engine { return r.eng }

// Stats returns what the loop did in this process.
func (r *Runner) Stats() Stats {
	st := r.stats
	st.Rejections, st.Unreached = r.send.Rejections(), r.send.Unreached()
	return st
}
