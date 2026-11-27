package store

// ActorRow is one actor in the read model.
// Profile is the profile the actor was spawned with. Genesis actors are not
// spawned in the log, so the store is the only source for theirs.
type ActorRow struct {
	ID      uint64
	Name    string
	Role    string
	Profile string
	// Identity is the actor's public identity as JSON (addresses, key hashes,
	// traits); the store never interprets it. Empty for rows written before v3.
	Identity string
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
