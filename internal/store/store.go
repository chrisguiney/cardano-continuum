// Package store persists what the engine needs to survive a restart, in one
// SQLite file.
//
// The event log is authoritative and lives in internal/eventlog. The store
// holds a checkpoint: the chain cursor (intersection points), the engine's
// snapshot and the log seq both reflect. Recovery is the checkpoint plus a
// replay of the log after that seq.
//
// Three rules keep recovery sound:
//   - The log goes first: SaveCheckpoint refuses a seq above the log's
//     durable seq, so a checkpoint never reflects an input a crash could lose.
//   - A checkpoint is one transaction: points, actors and seq are never
//     visible separately.
//   - The stored points reach down to the confirmed point, so FindIntersect
//     never falls back to origin within the security parameter.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/rng"

	// Pure-Go SQLite driver: no cgo, so the binary cross-compiles and the
	// scratch images need no libc.
	_ "modernc.org/sqlite"
)

// Errors returned by the store.
var (
	ErrNoLog           = errors.New("store: no event log bound")
	ErrAheadOfLog      = errors.New("store: checkpoint seq above durable log seq")
	ErrCheckpointAhead = errors.New("store: checkpoint covers entries the log lacks")
	ErrPointsShort     = errors.New("store: intersection points do not reach the confirmed point")
	ErrSchemaTooNew    = errors.New("store: schema newer than this binary")
	ErrNoCheckpoint    = errors.New("store: no checkpoint")
	ErrBadSort         = errors.New("store: unknown sort key")
)

// SchemaVersion is the schema this binary writes. Older files are migrated
// forward on Open; newer ones are refused.
const SchemaVersion = 3

// migrations[i] upgrades schema version i to i+1.
var migrations = []string{`
CREATE TABLE meta (key TEXT PRIMARY KEY, value BLOB NOT NULL) WITHOUT ROWID;
CREATE TABLE checkpoint (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	seq INTEGER NOT NULL,
	confirmed_slot INTEGER NOT NULL,
	confirmed_hash BLOB NOT NULL,
	actors BLOB NOT NULL,
	intents_version INTEGER NOT NULL,
	intents BLOB NOT NULL,
	reservations_version INTEGER NOT NULL,
	reservations BLOB NOT NULL
);
CREATE TABLE point (
	idx INTEGER PRIMARY KEY,
	slot INTEGER NOT NULL,
	hash BLOB NOT NULL
);
CREATE TABLE actor (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	role TEXT NOT NULL
);`,
	// v2: genesis actors carry their profile in the store, since the log has
	// no Spawned record for them. Rows written before v2 keep the empty profile.
	`ALTER TABLE actor ADD COLUMN profile TEXT NOT NULL DEFAULT '';`,
	// v3: the actor's public identity (addresses, key hashes, traits) as JSON.
	`ALTER TABLE actor ADD COLUMN identity TEXT NOT NULL DEFAULT '';`}

// Failpoint stages inside SaveCheckpoint, after the named write.
const (
	stageCheckpoint = "checkpoint"
	stagePoints     = "points"
	stageActors     = "actors"
	stageCommit     = "commit"
)

// DurableLog is the event log as the store sees it.
type DurableLog interface {
	eventlog.Reader
	// DurableSeq is the seq of the last fsynced entry.
	DurableSeq() uint64
	// LastSeq is the seq of the last entry, or 0 if empty.
	LastSeq() uint64
}

// Option configures Open.
type Option func(*DB)

// WithLog binds the event log that checkpoints are validated against and
// recovery replays from. Without it a checkpoint may only cover seq 0.
func WithLog(l DurableLog) Option { return func(d *DB) { d.log = l } }

// withFault installs a failpoint called after each write inside
// SaveCheckpoint; a non-nil error aborts the transaction.
func withFault(f func(stage string) error) Option { return func(d *DB) { d.fault = f } }

// DB is the store. It holds one connection, so writes are serialised and a
// transaction is never interleaved with another.
type DB struct {
	db    *sql.DB
	log   DurableLog
	fault func(stage string) error
}

// Open opens or creates the database at path (mode 0600), enables WAL and
// full fsync, and migrates the schema.
func Open(ctx context.Context, path string, opts ...Option) (*DB, error) {
	// Create the file before SQLite does, so it is never group or world
	// readable: it holds the seed.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "synchronous(FULL)", "busy_timeout(5000)", "foreign_keys(1)"} {
		q.Add("_pragma", p)
	}
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath(), RawQuery: q.Encode()}
	sdb, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	sdb.SetMaxOpenConns(1)
	d := &DB{db: sdb}
	for _, o := range opts {
		o(d)
	}
	if err := d.migrate(ctx); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// SchemaVersion returns the stored schema version.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}

func (d *DB) migrate(ctx context.Context) error {
	v, err := d.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if v > SchemaVersion {
		return fmt.Errorf("%w: file is v%d, binary is v%d", ErrSchemaTooNew, v, SchemaVersion)
	}
	for ; v < SchemaVersion; v++ {
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// user_version is part of the transaction, so a crash leaves the
		// schema and its version together.
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate v%d: %w", v+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set schema version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Seed is the root seed. It prints as redacted in every format.
type Seed struct{ s rng.Seed }

// Root returns the raw seed. Callers derive streams from it and never log it.
func (s Seed) Root() rng.Seed { return s.s }

// String implements fmt.Stringer.
func (Seed) String() string { return "store.Seed(redacted)" }

// GoString implements fmt.GoStringer.
func (Seed) GoString() string { return "store.Seed(redacted)" }

// Format implements fmt.Formatter, covering every verb.
func (Seed) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte("store.Seed(redacted)")) }

// MarshalJSON implements json.Marshaler.
func (Seed) MarshalJSON() ([]byte, error) { return []byte(`"redacted"`), nil }

// Seed returns the root seed, generating and storing it on first use.
func (d *DB) Seed(ctx context.Context) (Seed, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return Seed{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var b []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'seed'").Scan(&b)
	switch {
	case err == nil:
		var s Seed
		if len(b) != len(s.s) {
			return Seed{}, fmt.Errorf("stored seed has %d bytes, want %d", len(b), len(s.s))
		}
		copy(s.s[:], b)
		return s, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Seed{}, fmt.Errorf("read seed: %w", err)
	}
	var s Seed
	if _, err := rand.Read(s.s[:]); err != nil {
		return Seed{}, fmt.Errorf("generate seed: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES ('seed', ?)", s.s[:]); err != nil {
		return Seed{}, fmt.Errorf("store seed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Seed{}, err
	}
	return s, nil
}

// Actors returns one page of the actor read model, ordered by q.SortBy with
// id as the tiebreak.
func (d *DB) Actors(ctx context.Context, q ActorQuery) ([]ActorRow, error) {
	col, ok := sortColumns[q.SortBy]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrBadSort, q.SortBy)
	}
	if q.Offset < 0 || q.Limit < 0 || q.Limit > MaxLimit {
		return nil, fmt.Errorf("store: bad page offset %d limit %d", q.Offset, q.Limit)
	}
	limit := q.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	rows, err := d.db.QueryContext(ctx,
		"SELECT id, name, role, profile, identity FROM actor ORDER BY "+col+" "+dir+", id ASC LIMIT ? OFFSET ?", limit, q.Offset)
	if err != nil {
		return nil, fmt.Errorf("query actors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ActorRow
	for rows.Next() {
		var a ActorRow
		if err := rows.Scan(&a.ID, &a.Name, &a.Role, &a.Profile, &a.Identity); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) failpoint(stage string) error {
	if d.fault == nil {
		return nil
	}
	return d.fault(stage)
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
				if _, err := tx.ExecContext(ctx, "INSERT INTO actor (id, name, role, profile, identity) VALUES (?, ?, ?, ?, ?)", a.ID, a.Name, a.Role, a.Profile, a.Identity); err != nil {
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
	rows, err = tx.QueryContext(ctx, "SELECT id, name, role, profile, identity FROM actor ORDER BY id")
	if err != nil {
		return Checkpoint{}, fmt.Errorf("load actors: %w", err)
	}
	for rows.Next() {
		var a ActorRow
		if err := rows.Scan(&a.ID, &a.Name, &a.Role, &a.Profile, &a.Identity); err != nil {
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
