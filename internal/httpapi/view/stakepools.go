package view

import (
	"cmp"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/btcsuite/btcd/btcutil/bech32"

	"github.com/chrisguiney/cardano-continuum/internal/explorer"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// StakePoolRow is one stake pool: an on-chain registration, operated by a
// pool actor or seen only on chain. The nodes that run its keys are listed;
// none means it exists only as certificates (a logical pool).
type StakePoolRow struct {
	// ID is the pool id, hex.
	ID string `json:"id"`
	// Bech32 is the pool id as pool1...
	Bech32 string `json:"bech32"`
	// Actor is the pool actor operating it; nil when no actor does.
	Actor *uint64 `json:"actor"`
	// ActorName, Profile and ActorState are the operator actor's name,
	// profile and lifecycle state (registered, retiring, retired, ...).
	ActorName  string `json:"actor_name,omitempty"`
	Profile    string `json:"profile,omitempty"`
	ActorState string `json:"actor_state,omitempty"`
	// Registration is the latest registration certificate the explorer's
	// index saw; nil for a genesis pool or without the explorer.
	Registration *explorer.PoolReg `json:"registration"`
	// RetireEpoch is the epoch a retirement certificate takes effect; nil
	// when none is in force.
	RetireEpoch *uint64 `json:"retire_epoch"`
	// Delegators counts the actors delegating to the pool, and
	// DelegatedLovelace sums their settled lovelace: the engine's planner
	// ledger, not the ledger's stake snapshot.
	Delegators        int    `json:"delegators"`
	DelegatedLovelace uint64 `json:"delegated_lovelace"`
	// Blocks counts the blocks the pool issued; nil without the explorer.
	Blocks *uint64 `json:"blocks"`
	// Votes counts the pool's governance votes in force.
	Votes int `json:"votes"`
	// Nodes are the nodes running the pool's keys, by name.
	Nodes []NodeRef `json:"nodes"`
}

// NodeRef names a node from a stake pool's row.
type NodeRef struct {
	// Name is the node's name.
	Name string `json:"name"`
	// Impl, Tier and Phase are as in NodeRow.
	Impl  string `json:"impl"`
	Tier  string `json:"tier"`
	Phase string `json:"phase"`
}

// StakePoolsView is the Stake pools view.
type StakePoolsView struct {
	// Pools are the rows: by operator actor, then pools no actor operates,
	// by id.
	Pools []StakePoolRow `json:"pools"`
}

// stakeInputs is what buildStakePools reads.
type stakeInputs struct {
	// names and idents are the store's actor rows and parsed identities.
	names  map[uint64]store.ActorRow
	idents map[uint64]store.Identity
	// states are the actors' lifecycle states from the log.
	states map[uint64]string
	// votes counts current SPO votes by pool id (hex).
	votes map[string]int
}

// buildStakePools makes a row for every pool actor. Pure.
func buildStakePools(in stakeInputs) StakePoolsView {
	rows := map[string]*StakePoolRow{}
	for id, ident := range in.idents {
		hexID, ok := PoolHex(ident.PoolID)
		if !ok {
			continue
		}
		actor := id
		rows[hexID] = &StakePoolRow{
			ID:         hexID,
			Bech32:     ident.PoolID,
			Actor:      &actor,
			ActorName:  in.names[id].Name,
			Profile:    in.names[id].Profile,
			ActorState: in.states[id],
			Votes:      in.votes[hexID],
			Nodes:      []NodeRef{},
		}
	}

	for _, ident := range in.idents {
		hexID, ok := PoolHex(ident.DelegatedPool)
		if !ok || rows[hexID] == nil {
			continue
		}
		rows[hexID].Delegators++
		rows[hexID].DelegatedLovelace += ident.Settled
	}

	return StakePoolsView{Pools: sortedRows(rows)}
}

// sortedRows lists rows by operator actor, then the rows no actor operates
// by id.
func sortedRows(rows map[string]*StakePoolRow) []StakePoolRow {
	out := make([]StakePoolRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b StakePoolRow) int {
		switch {
		case a.Actor != nil && b.Actor != nil:
			if c := cmp.Compare(*a.Actor, *b.Actor); c != 0 {
				return c
			}
		case a.Actor != nil:
			return -1
		case b.Actor != nil:
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// WithNodes returns v with each node that runs a stake pool's keys listed
// on that pool's row, adding a row for a pool no actor operates. v is not
// changed.
func WithNodes(v StakePoolsView, nodes NodesView) StakePoolsView {
	rows := map[string]*StakePoolRow{}
	for _, p := range v.Pools {
		p.Nodes = slices.Clone(p.Nodes)
		rows[p.ID] = &p
	}

	for _, n := range nodes.Nodes {
		if n.StakePool == "" {
			continue
		}
		r := rows[n.StakePool]
		if r == nil {
			r = &StakePoolRow{ID: n.StakePool, Bech32: PoolBech32(n.StakePool), Nodes: []NodeRef{}}
			rows[n.StakePool] = r
		}
		r.Nodes = append(r.Nodes, NodeRef{Name: n.Name, Impl: n.Impl, Tier: n.Tier, Phase: n.Phase})
	}
	return StakePoolsView{Pools: sortedRows(rows)}
}

// WithChain returns v with the explorer's view of each pool: blocks issued,
// registration and retirement, adding a row for a pool only the chain
// shows. v is not changed.
func WithChain(v StakePoolsView, stats []explorer.PoolStat) StakePoolsView {
	rows := map[string]*StakePoolRow{}
	for _, p := range v.Pools {
		p.Nodes = slices.Clone(p.Nodes)
		rows[p.ID] = &p
	}

	for _, s := range stats {
		r := rows[s.ID]
		if r == nil {
			r = &StakePoolRow{ID: s.ID, Bech32: PoolBech32(s.ID), Nodes: []NodeRef{}}
			rows[s.ID] = r
		}
		blocks := s.Blocks
		r.Blocks, r.Registration, r.RetireEpoch = &blocks, s.Reg, s.RetireEpoch
	}

	for _, r := range rows {
		if r.Blocks == nil {
			zero := uint64(0)
			r.Blocks = &zero
		}
	}
	return StakePoolsView{Pools: sortedRows(rows)}
}

// PoolBech32 is a hex pool id as pool1...; "" for one that is not.
func PoolBech32(id string) string {
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != 28 {
		return ""
	}
	conv, err := bech32.ConvertBits(b, 8, 5, true)
	if err != nil {
		return ""
	}
	s, err := bech32.Encode("pool", conv)
	if err != nil {
		return ""
	}
	return s
}

// PoolHex is a pool1... id as hex, if it is one.
func PoolHex(s string) (string, bool) {
	if !strings.HasPrefix(s, "pool1") {
		return "", false
	}
	hrp, data, err := bech32.Decode(s)
	if err != nil || hrp != "pool" {
		return "", false
	}
	b, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil || len(b) != 28 {
		return "", false
	}
	return hex.EncodeToString(b), true
}
