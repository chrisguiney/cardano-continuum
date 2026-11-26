package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// ChainEvent is one chain-sync message as the loop reads it.
type ChainEvent struct {
	// Rollback is true for a roll-backward to Point, false for a block at
	// Point whose digest is Content.
	Rollback bool
	Point    eventlog.ChainPoint
	Content  []byte
}

// Follower delivers chain-sync messages. Next returns false when caught up.
type Follower interface {
	Next() (ChainEvent, bool, error)
}

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
type Checkpointer interface {
	SaveCheckpoint(ctx context.Context, c store.Checkpoint) error
	Recover(ctx context.Context) (store.Recovery, error)
}

// Crash stages: the points at which Deps.Crash may stop the loop.
const (
	StageFed          = "fed"          // an input and its decisions are appended
	StageSubmitted    = "submitted"    // a transaction reached the node, its result is not logged
	StageSynced       = "synced"       // the log is flushed before a checkpoint
	StageCheckpointed = "checkpointed" // a checkpoint is committed
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
	// Crash, when set, is called at every stage; a non-nil error stops the
	// loop on the spot, as a kill would. Tests use it for crash injection.
	Crash  func(stage string) error
	Logger *slog.Logger
}

// ErrDiverged: replaying the log on restart produced different decisions,
// so the log was written by a different engine or the engine is not
// deterministic.
var ErrDiverged = errors.New("engine: replay diverged from the log")

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
	// resumed are the intents in flight when this process started.
	resumed map[uint64]bool
	since   int
	stats   Stats
	// plan is the pool plan last synced.
	plan   []PoolPlan
	synced bool
	log    *slog.Logger
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
	var snap []byte
	var covers uint64
	if d.Store != nil {
		rec, err := d.Store.Recover(ctx)
		if err != nil {
			return nil, fmt.Errorf("engine: recover: %w", err)
		}
		if rec.Checkpoint.Seq > 0 {
			snap, covers = rec.Checkpoint.Actors, rec.Checkpoint.Seq
		}
	}
	if d.Log.LastSeq() == 0 {
		if err := writeHeader(ctx, d.Log, hdr); err != nil {
			return nil, err
		}
	} else if err := checkHeader(ctx, d.Log, hdr); err != nil {
		return nil, err
	}
	if err := r.replay(ctx, snap, covers); err != nil {
		return nil, err
	}
	r.sess = eventlog.Session{Eng: r.eng, Log: d.Log}
	// A payment in flight at a restart may have been sent by the process
	// that died, so a rejection of it proves nothing.
	r.resumed = map[uint64]bool{}
	for _, s := range r.eng.InFlight() {
		r.resumed[s.Intent] = true
	}
	r.log.Info("engine started", "covers", covers, "log_seq", d.Log.LastSeq(), "actors", r.eng.Actors())
	if err := r.effects(ctx); err != nil {
		return nil, err
	}
	if err := r.intersect(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func writeHeader(ctx context.Context, l Log, h eventlog.HeaderBody) error {
	body, err := eventlog.Encode(h)
	if err != nil {
		return err
	}
	if err := l.Append(ctx, eventlog.Record{Class: eventlog.Header, Kind: eventlog.KindHeader, Body: body}); err != nil {
		return fmt.Errorf("engine: write header: %w", err)
	}
	return l.Sync(ctx)
}

func checkHeader(ctx context.Context, l eventlog.Reader, want eventlog.HeaderBody) error {
	var got []byte
	err := l.Scan(ctx, 1, func(rec eventlog.Record) error {
		if rec.Class == eventlog.Header {
			got = rec.Body
		}
		return eventlog.ErrStop
	})
	if err != nil {
		return err
	}
	w, err := eventlog.Encode(want)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, w) {
		return fmt.Errorf("%w: log was recorded under another seed, profile set, genesis or binary", eventlog.ErrHeaderMismatch)
	}
	return nil
}

// replay rebuilds the engine from the checkpoint and the log tail. Decisions
// of the last input may be missing (they are appended unsynced and a crash
// can drop them); those are regenerated and appended. Any other difference
// is divergence.
func (r *Runner) replay(ctx context.Context, snap []byte, covers uint64) error {
	factory := r.cfg.Factory()
	rp := eventlog.Replayer{Log: r.d.Log, New: func(s []byte) (eventlog.Engine, error) {
		e, err := factory(s)
		if err == nil {
			r.eng = e.(*Engine)
		}
		return e, err
	}}
	res, err := rp.Replay(ctx, snap, covers)
	if err != nil {
		return fmt.Errorf("engine: replay tail: %w", err)
	}
	if len(res.Diff) == 0 {
		return nil
	}
	var lastInput uint64
	err = r.d.Log.Scan(ctx, covers+1, func(rec eventlog.Record) error {
		if rec.Class == eventlog.Input {
			lastInput = rec.Seq
		}
		return nil
	})
	if err != nil {
		return err
	}
	var missing []eventlog.Record
	for _, m := range res.Diff {
		if m.Want != nil || m.Seq != lastInput {
			return fmt.Errorf("%w: %s", ErrDiverged, m)
		}
		missing = append(missing, *m.Got)
	}
	if err := r.d.Log.Append(ctx, missing...); err != nil {
		return err
	}
	return r.d.Log.Sync(ctx)
}

func (r *Runner) intersect(ctx context.Context) error {
	pts := r.eng.Points()
	slices.Reverse(pts)
	f, at, err := r.d.Node.Intersect(pts)
	if err != nil {
		return fmt.Errorf("engine: find intersect: %w", err)
	}
	r.follow = f
	wrote, err := r.sess.Intersect(ctx, r.d.Clock.Now(), r.eng.Tip(), at)
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

func (r *Runner) crash(stage string) error {
	if r.d.Crash == nil {
		return nil
	}
	return r.d.Crash(stage)
}

// step runs one logged input, then the post-input hooks.
func (r *Runner) step(ctx context.Context, feed func() error) error {
	if err := feed(); err != nil {
		return err
	}
	r.stats.Inputs++
	r.since++
	if err := r.crash(StageFed); err != nil {
		return err
	}
	if r.eng.Stalled() {
		return ErrStalled
	}
	if r.d.CheckpointEvery > 0 && r.since >= r.d.CheckpointEvery {
		return r.Checkpoint(ctx)
	}
	return nil
}

// effects submits every payment the engine is waiting on, oldest first, and
// logs each answer as a SubmitResult input. The Submitted decision, which
// carries the reservation, is synced before its transaction leaves.
func (r *Runner) effects(ctx context.Context) error {
	for {
		inf := r.eng.InFlight()
		if len(inf) == 0 {
			return nil
		}
		s := inf[0]
		res, err := r.send(ctx, s)
		if err != nil {
			return err
		}
		delete(r.resumed, s.Intent)
		r.stats.Submissions++
		if err := r.crash(StageSubmitted); err != nil {
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

// send submits s unless the mempool already holds it, and classifies the
// answer with internal/submit.
func (r *Runner) send(ctx context.Context, s Submission) (SubmitResult, error) {
	res := SubmitResult{Intent: s.Intent, Verdict: VerdictAccepted}
	if r.d.Node.HasTx(s.ID) {
		return res, nil
	}
	if err := r.d.Log.Sync(ctx); err != nil {
		return res, fmt.Errorf("engine: sync before submit: %w", err)
	}
	o := submit.Send(ctx, nodeTarget{r.d.Node}, 0, s.Tx)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	switch o.Verdict {
	case submit.Rejected:
		res.Verdict, res.Reason = VerdictRejected, (&submit.RejectedError{Failures: o.Failures}).Error()
		for _, f := range o.Failures {
			res.Failures = append(res.Failures, string(f.Class))
		}
		res.Resend = r.resumed[s.Intent]
		r.stats.Rejections++
	case submit.Unreached:
		res.Verdict, res.Reason = VerdictUnreached, o.Err.Error()
		r.stats.Unreached++
	}
	return res, nil
}

// nodeTarget lets internal/submit classify a Node's answer. The era is the
// Node's concern.
type nodeTarget struct{ n Node }

func (t nodeTarget) Submit(ctx context.Context, _ uint, tx []byte) error { return t.n.Submit(ctx, tx) }

func (nodeTarget) Name() string { return "node" }

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
		id, ok := r.eng.IntentOf(tx)
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
	plan := r.eng.Pools()
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
	if r.d.Store == nil {
		return r.d.Log.Sync(ctx)
	}
	return r.Checkpoint(ctx)
}

// Checkpoint flushes the log and then saves the engine state as covering
// every entry, in that order: a checkpoint never reflects an input the
// durable log lacks.
func (r *Runner) Checkpoint(ctx context.Context) error {
	if err := r.d.Log.Sync(ctx); err != nil {
		return fmt.Errorf("engine: sync log: %w", err)
	}
	if r.d.Store == nil {
		return nil
	}
	if err := r.crash(StageSynced); err != nil {
		return err
	}
	snap, err := r.eng.Snapshot()
	if err != nil {
		return err
	}
	res, err := r.eng.Reservations()
	if err != nil {
		return err
	}
	c := store.Checkpoint{
		Seq: r.d.Log.LastSeq(), Points: r.eng.Points(), Confirmed: r.eng.Confirmed(),
		Actors: snap, ActorRows: r.eng.ActorRows(), Reservations: res,
	}
	if err := r.d.Store.SaveCheckpoint(ctx, c); err != nil {
		return fmt.Errorf("engine: checkpoint: %w", err)
	}
	r.since = 0
	return r.crash(StageCheckpointed)
}

// Engine returns the engine the loop drives.
func (r *Runner) Engine() *Engine { return r.eng }

// Stats returns what the loop did in this process.
func (r *Runner) Stats() Stats { return r.stats }
