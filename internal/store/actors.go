package store

import (
	"context"
	"fmt"
)

// ActorRow is one actor in the read model.
// Profile is the profile the actor was spawned with. Genesis actors are not
// spawned in the log, so the store is the only source for theirs.
type ActorRow struct {
	ID      uint64
	Name    string
	Role    string
	Profile string
}

// ActorQuery pages the actor read model.
type ActorQuery struct {
	SortBy string // one of id, name, role
	Desc   bool
	Offset int
	Limit  int // 0 means DefaultLimit
}

// Paging bounds for Actors.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// sortColumns is the only source of ORDER BY text; the key from a caller is
// looked up here and never spliced into SQL.
var sortColumns = map[string]string{"id": "id", "name": "name", "role": "role"}

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
		"SELECT id, name, role, profile FROM actor ORDER BY "+col+" "+dir+", id ASC LIMIT ? OFFSET ?", limit, q.Offset)
	if err != nil {
		return nil, fmt.Errorf("query actors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ActorRow
	for rows.Next() {
		var a ActorRow
		if err := rows.Scan(&a.ID, &a.Name, &a.Role, &a.Profile); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
