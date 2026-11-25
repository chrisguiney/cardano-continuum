package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// Blob is an opaque, versioned value owned by another component. The store
// never interprets Data; the owner reads Version to migrate it.
type Blob struct {
	Version uint32
	Data    []byte
}

// Checkpoint is everything recovery loads before replaying the log tail.
type Checkpoint struct {
	// Seq is the last log seq the checkpoint reflects; 0 means none.
	Seq uint64
	// Points are the intersection points, oldest first. They must reach
	// down to Confirmed.
	Points []eventlog.ChainPoint
	// Confirmed is the K-deep point durable actor state comes from.
	Confirmed eventlog.ChainPoint
	// Actors is the engine snapshot, in the engine's canonical encoding.
	Actors []byte
	// ActorRows is the read model the UI pages through.
	ActorRows []ActorRow
	// Intents and Reservations are opaque to the store. Reservations stay
	// an opaque blob until the planner-reservations contract defines their
	// shape.
	Intents      Blob
	Reservations Blob
}

func (d *DB) failpoint(stage string) error {
	if d.fault == nil {
		return nil
	}
	return d.fault(stage)
}

func containsPoint(pts []eventlog.ChainPoint, p eventlog.ChainPoint) bool {
	for _, q := range pts {
		if q.Slot == p.Slot && bytes.Equal(q.Hash, p.Hash) {
			return true
		}
	}
	return false
}

func (d *DB) validate(c Checkpoint) error {
	if c.Seq > 0 {
		if d.log == nil {
			return ErrNoLog
		}
		if dur := d.log.DurableSeq(); c.Seq > dur {
			return fmt.Errorf("%w: seq %d, durable %d", ErrAheadOfLog, c.Seq, dur)
		}
	}
	zero := c.Confirmed.Slot == 0 && len(c.Confirmed.Hash) == 0
	switch {
	case len(c.Points) == 0 && !zero:
		return fmt.Errorf("%w: confirmed slot %d, no points", ErrPointsShort, c.Confirmed.Slot)
	case len(c.Points) > 0 && !containsPoint(c.Points, c.Confirmed):
		return fmt.Errorf("%w: confirmed slot %d not among %d points", ErrPointsShort, c.Confirmed.Slot, len(c.Points))
	}
	return nil
}

// SaveCheckpoint replaces the checkpoint in one transaction. It refuses a seq
// above the log's durable seq and points that do not reach c.Confirmed.
func (d *DB) SaveCheckpoint(ctx context.Context, c Checkpoint) error {
	if err := d.validate(c); err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	steps := []struct {
		stage string
		do    func() error
	}{
		{stageCheckpoint, func() error {
			_, err := tx.ExecContext(ctx, `INSERT INTO checkpoint
(id, seq, confirmed_slot, confirmed_hash, actors, intents_version, intents, reservations_version, reservations)
VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET seq = excluded.seq, confirmed_slot = excluded.confirmed_slot,
confirmed_hash = excluded.confirmed_hash, actors = excluded.actors,
intents_version = excluded.intents_version, intents = excluded.intents,
reservations_version = excluded.reservations_version, reservations = excluded.reservations`,
				c.Seq, c.Confirmed.Slot, nonNil(c.Confirmed.Hash), nonNil(c.Actors),
				c.Intents.Version, nonNil(c.Intents.Data), c.Reservations.Version, nonNil(c.Reservations.Data))
			return err
		}},
		{stagePoints, func() error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM point"); err != nil {
				return err
			}
			for i, p := range c.Points {
				if _, err := tx.ExecContext(ctx, "INSERT INTO point (idx, slot, hash) VALUES (?, ?, ?)", i, p.Slot, nonNil(p.Hash)); err != nil {
					return err
				}
			}
			return nil
		}},
		{stageActors, func() error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM actor"); err != nil {
				return err
			}
			for _, a := range c.ActorRows {
				if _, err := tx.ExecContext(ctx, "INSERT INTO actor (id, name, role) VALUES (?, ?, ?)", a.ID, a.Name, a.Role); err != nil {
					return err
				}
			}
			return nil
		}},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			return fmt.Errorf("save checkpoint %s: %w", s.stage, err)
		}
		if err := d.failpoint(s.stage); err != nil {
			return fmt.Errorf("save checkpoint %s: %w", s.stage, err)
		}
	}
	if err := d.failpoint(stageCommit); err != nil {
		return fmt.Errorf("save checkpoint %s: %w", stageCommit, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save checkpoint commit: %w", err)
	}
	return nil
}

// nonNil keeps NULL out of NOT NULL blob columns.
func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// LoadCheckpoint returns the stored checkpoint, or ErrNoCheckpoint.
func (d *DB) LoadCheckpoint(ctx context.Context) (Checkpoint, error) {
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Checkpoint{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var c Checkpoint
	err = tx.QueryRowContext(ctx, `SELECT seq, confirmed_slot, confirmed_hash, actors,
intents_version, intents, reservations_version, reservations FROM checkpoint WHERE id = 1`).
		Scan(&c.Seq, &c.Confirmed.Slot, &c.Confirmed.Hash, &c.Actors,
			&c.Intents.Version, &c.Intents.Data, &c.Reservations.Version, &c.Reservations.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, ErrNoCheckpoint
	}
	if err != nil {
		return Checkpoint{}, fmt.Errorf("load checkpoint: %w", err)
	}
	if len(c.Confirmed.Hash) == 0 {
		c.Confirmed.Hash = nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT slot, hash FROM point ORDER BY idx")
	if err != nil {
		return Checkpoint{}, fmt.Errorf("load points: %w", err)
	}
	for rows.Next() {
		var p eventlog.ChainPoint
		if err := rows.Scan(&p.Slot, &p.Hash); err != nil {
			_ = rows.Close()
			return Checkpoint{}, err
		}
		c.Points = append(c.Points, p)
	}
	if err := rows.Close(); err != nil {
		return Checkpoint{}, err
	}
	if err := rows.Err(); err != nil {
		return Checkpoint{}, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT id, name, role FROM actor ORDER BY id")
	if err != nil {
		return Checkpoint{}, fmt.Errorf("load actors: %w", err)
	}
	for rows.Next() {
		var a ActorRow
		if err := rows.Scan(&a.ID, &a.Name, &a.Role); err != nil {
			_ = rows.Close()
			return Checkpoint{}, err
		}
		c.ActorRows = append(c.ActorRows, a)
	}
	if err := rows.Close(); err != nil {
		return Checkpoint{}, err
	}
	return c, rows.Err()
}

// Recovery is what the engine needs to resume.
type Recovery struct {
	// Checkpoint is the stored checkpoint, zero if none was saved.
	Checkpoint Checkpoint
	// Points are the intersection points after folding in the tail, oldest
	// first; they are what the restart's FindIntersect offers.
	Points []eventlog.ChainPoint
	// Tail is every log record after Checkpoint.Seq, in order. Feed it to
	// the engine built from Checkpoint.Actors.
	Tail []eventlog.Record
}

// Recover loads the checkpoint and the log tail after its seq. A checkpoint
// covering entries the log no longer has is an error: the log is
// authoritative and a state it cannot reproduce is not trusted.
func (d *DB) Recover(ctx context.Context) (Recovery, error) {
	if d.log == nil {
		return Recovery{}, ErrNoLog
	}
	c, err := d.LoadCheckpoint(ctx)
	if err != nil && !errors.Is(err, ErrNoCheckpoint) {
		return Recovery{}, err
	}
	if last := d.log.LastSeq(); c.Seq > last {
		return Recovery{}, fmt.Errorf("%w: checkpoint seq %d, log ends at %d", ErrCheckpointAhead, c.Seq, last)
	}
	rec := Recovery{Checkpoint: c, Points: append([]eventlog.ChainPoint(nil), c.Points...)}
	err = d.log.Scan(ctx, c.Seq+1, func(r eventlog.Record) error {
		pts, err := eventlog.ApplyPoint(rec.Points, r)
		if err != nil {
			return fmt.Errorf("fold seq %d: %w", r.Seq, err)
		}
		rec.Points = pts
		rec.Tail = append(rec.Tail, r)
		return nil
	})
	if err != nil {
		return Recovery{}, fmt.Errorf("scan log tail: %w", err)
	}
	return rec, nil
}
