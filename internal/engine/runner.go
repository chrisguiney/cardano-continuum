package engine

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/feeds"
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

// Pacer drives a chain that has no clock of its own (the fake chain).
type Pacer interface {
	Advance(t sched.Time) error
	// NextChange is the next time after now at which the chain may change.
	NextChange(now sched.Time) sched.Time
}

// The runner's optional feeds live in internal/engine/internal/feeds.
type (
	// PodSource reports the pool pods' observed health; see
	// feeds.PodSource.
	PodSource = feeds.PodSource
	// ControlSource hands the loop the submitted controls; see
	// feeds.ControlSource.
	ControlSource = feeds.ControlSource
	// Dropper is a Node that reports mempool drops; see feeds.Dropper.
	Dropper = feeds.Dropper
	// PoolWriter applies the pool plan; see feeds.PoolWriter.
	PoolWriter = feeds.PoolWriter
)

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
	feeds  feeds.Set
	stats  Stats
	log    *slog.Logger
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
	if r.eng, err = replay(ctx, cfg, d, snap, covers); err != nil {
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
	r.feeds = feedsOf(r.eng, d, r.log)
	r.log.Info("engine started", "covers", covers, "log_seq", d.Log.LastSeq(), "actors", r.eng.Summary().Actors)
	if err := r.effects(ctx); err != nil {
		return nil, err
	}
	if err := r.intersect(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// replay rebuilds the engine from snap and the log tail after covers.
func replay(ctx context.Context, cfg Config, d Deps, snap []byte, covers uint64) (*Engine, error) {
	var eng *Engine
	factory := cfg.Factory()
	f := func(s []byte) (eventlog.Engine, error) {
		e, err := factory(s)
		if err != nil {
			return e, err
		}
		eng = e.(*Engine)
		if d.Findings != nil {
			eng.jd.Use(d.Findings)
		}
		// The checkpoint's findings are in the log, not the snapshot; the
		// tail's are regenerated by the replay that follows.
		return e, restart.LoadFindings(ctx, d.Log, covers, eng.jd.Set())
	}
	if err := restart.Replay(ctx, d.Log, snap, covers, f); err != nil {
		return nil, err
	}
	return eng, nil
}

// feedsOf builds the run's feeds over e; a feed whose source d lacks
// does nothing.
func feedsOf(e *Engine, d Deps, log *slog.Logger) feeds.Set {
	dropper, _ := d.Node.(Dropper)
	return feeds.Set{
		View:     feeds.NewView(d.Viewer, settled{e}, d.Clock, d.ViewEvery, log),
		Tips:     feeds.NewTips(d.Tips, d.Clock, d.ViewEvery),
		Pods:     feeds.NewPods(d.Pods, d.Clock),
		Drops:    feeds.NewDrops(dropper, e.pay),
		Controls: feeds.NewControls(d.Controls),
		Pools:    feeds.NewPools(d.Pools, e.st),
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
		if err := r.feeds.Poll(ctx, loop{r}); err != nil {
			return err
		}
		now := r.d.Clock.Now()
		ticked, err := r.tickDue(ctx, now)
		if err != nil {
			return err
		}
		if ticked {
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
			return r.ckpt.Flush(ctx, stateOf(r.eng))
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
	if err := r.ckpt.Flush(context.WithoutCancel(ctx), stateOf(r.eng)); err != nil {
		return err
	}
	return ctx.Err()
}

// tickDue feeds a Tick at now when the engine's next event is due, then
// the submissions it decides, and reports whether it did.
func (r *Runner) tickDue(ctx context.Context, now sched.Time) (bool, error) {
	if t, ok := r.eng.NextEvent(); !ok || t > now {
		return false, nil
	}
	r.stats.Ticks++
	if err := r.step(ctx, func() error { return r.sess.Tick(ctx, now) }); err != nil {
		return true, err
	}
	return true, r.effects(ctx)
}

// Checkpoint flushes the log and then saves the engine state as covering
// every entry, in that order: a checkpoint never reflects an input the
// durable log lacks.
func (r *Runner) Checkpoint(ctx context.Context) error {
	return r.ckpt.Save(ctx, stateOf(r.eng))
}

// stateOf builds e's checkpoint covering the log up to a seq.
func stateOf(e *Engine) restart.State {
	return func(seq uint64) (store.Checkpoint, error) {
		snap, err := e.Snapshot()
		if err != nil {
			return store.Checkpoint{}, err
		}
		res, err := e.pay.Blob()
		if err != nil {
			return store.Checkpoint{}, err
		}
		return store.Checkpoint{
			Seq: seq, Points: e.win.Points(), Confirmed: e.win.Confirmed(),
			Actors: snap, ActorRows: actorRows(e), Reservations: res,
		}, nil
	}
}

// Engine returns the engine the loop drives.
func (r *Runner) Engine() *Engine { return r.eng }

// Stats returns what the loop did in this process.
func (r *Runner) Stats() Stats {
	st := r.stats
	st.Rejections, st.Unreached = r.send.Rejections(), r.send.Unreached()
	return st
}

// loop is the runner as a feed uses it.
type loop struct{ r *Runner }

func (l loop) Feed(ctx context.Context, rec eventlog.Record) error {
	return l.r.step(ctx, func() error { return l.r.sess.Feed(ctx, rec) })
}

func (l loop) Effects(ctx context.Context) error { return l.r.effects(ctx) }

func (l loop) Now() sched.Time { return l.r.eng.Now() }
