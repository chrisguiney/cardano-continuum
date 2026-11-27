package recovery

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// State builds the checkpoint that covers the log up to seq.
type State func(seq uint64) (store.Checkpoint, error)

// Saver counts inputs between checkpoints and commits each checkpoint
// after the log batch it covers, so a checkpoint never reflects an input
// the durable log lacks (the model's LogBeforeEffect).
type Saver struct {
	log   Log
	store Store
	crash func(stage string) error
	every int
	since int
}

// NewSaver returns a Saver that checkpoints into s every every inputs
// (zero: only on Flush). A nil s keeps no checkpoints; crash, when set, is
// called at every stage and a non-nil error stops the loop, as a kill
// would.
func NewSaver(l Log, s Store, every int, crash func(stage string) error) *Saver {
	return &Saver{log: l, store: s, crash: crash, every: every}
}

// Crash calls the crash hook at stage.
func (s *Saver) Crash(stage string) error {
	if s.crash == nil {
		return nil
	}
	return s.crash(stage)
}

// Fed records that one input and its decisions were appended and reports
// whether a checkpoint is due.
func (s *Saver) Fed() (bool, error) {
	s.since++
	if err := s.Crash(StageFed); err != nil {
		return false, err
	}
	return s.every > 0 && s.since >= s.every, nil
}

// Save flushes the log and then saves the checkpoint state builds as
// covering every entry, in that order.
func (s *Saver) Save(ctx context.Context, state State) error {
	if err := s.log.Sync(ctx); err != nil {
		return fmt.Errorf("engine: sync log: %w", err)
	}
	if s.store == nil {
		return nil
	}
	if err := s.Crash(StageSynced); err != nil {
		return err
	}
	c, err := state(s.log.LastSeq())
	if err != nil {
		return err
	}
	if err := s.store.SaveCheckpoint(ctx, c); err != nil {
		return fmt.Errorf("engine: checkpoint: %w", err)
	}
	s.since = 0
	return s.Crash(StageCheckpointed)
}

// Flush leaves nothing for a restart to replay: it checkpoints, or only
// syncs the log when there is no store.
func (s *Saver) Flush(ctx context.Context, state State) error {
	if s.store == nil {
		return s.log.Sync(ctx)
	}
	return s.Save(ctx, state)
}
