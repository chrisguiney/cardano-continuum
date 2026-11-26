package forger

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"

	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// shared is the state the server, the follower and the forge loop all
// touch. Every access goes through its methods, each one a critical section
// under mu; nothing outside holds the lock.
type shared struct {
	mu sync.Mutex // protects chain, sessions, synced, rogue
	// chain is every block known and each peer's view of it.
	chain    *Chain
	sessions map[string]*session
	// synced is true once the upstream has reported its tip reached.
	synced bool
	// rogue is the forged blocks no honest node may adopt, by hash.
	rogue map[[32]byte]Kind
	// changed wakes the loop that answers peers waiting at their tip.
	changed chan struct{}
}

// wake has the answer loop look at every waiting peer again.
func (s *shared) wake() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *shared) setSynced(v bool) {
	s.mu.Lock()
	s.synced = v
	s.mu.Unlock()
}

// points offers the upstream the followed tip, a few blocks behind it at
// doubling distances, and the origin.
func (s *shared) points() []pcommon.Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []pcommon.Point
	for back := 0; back < len(s.chain.followed); back = 2*back + 1 {
		e := s.chain.followed[len(s.chain.followed)-1-back]
		out = append(out, pcommon.NewPoint(e.Slot, e.Hash[:]))
	}
	return append(out, pcommon.NewPointOrigin())
}

// extend adds the upstream's next block. It marks the node synced when b is
// the upstream's tip, and reports the kind of b if the forger forged it as
// a block no honest node may adopt.
func (s *shared) extend(b Block, tip chainsync.Tip) (Kind, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.chain.Extend(b)
	kind, rogue := s.rogue[b.Hash]
	if string(tip.Point.Hash) == string(b.Hash[:]) {
		s.synced = true
	}
	return kind, rogue, err
}

func (s *shared) rollback(origin bool, hash [32]byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chain.Rollback(origin, hash)
}

func (s *shared) join(key string, peer PeerID) {
	s.mu.Lock()
	s.sessions[key] = &session{peer: peer, cursor: Cursor{Origin: true}}
	s.mu.Unlock()
}

func (s *shared) leave(key string) {
	s.mu.Lock()
	delete(s.sessions, key)
	s.mu.Unlock()
}

func (s *shared) rogueKind(hash [32]byte) (Kind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kind, ok := s.rogue[hash]
	return kind, ok
}

// add records a forged block.
func (s *shared) add(b Block, f Forge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chain.Add(b, f)
	if f.Expect == Reject {
		s.rogue[b.Hash] = f.Kind
	}
}

// plan is the state a forge at slot is planned from. It reports false until
// the upstream's tip is reached, or when the tip is not older than the
// slot: a block there would not be the block an honest node forges.
func (s *shared) plan(p Params, slot uint64) (State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tip := s.chain.Tip()
	if !s.synced || (!tip.Origin && tip.Slot >= slot) {
		return State{}, false
	}
	st := State{Slot: slot, Tip: tip, Nonce: s.chain.Nonces().Tick(p, slot).EpochNonce()}
	st.Counter, st.Forged = s.chain.Counter()
	for _, x := range s.sessions {
		if !slices.Contains(st.Peers, x.peer) {
			st.Peers = append(st.Peers, x.peer)
		}
	}
	slices.Sort(st.Peers)
	return st, true
}

// intersect answers a peer's find-intersect request.
func (s *shared) intersect(remote net.Addr, srv *chainsync.Server, points []pcommon.Point) (pcommon.Point, chainsync.Tip, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, err := lookup(s.sessions, remote)
	if err != nil {
		return pcommon.Point{}, chainsync.Tip{}, err
	}
	x.server = srv
	cursors := make([]Cursor, 0, len(points))
	for _, p := range points {
		c := Cursor{Origin: len(p.Hash) == 0}
		copy(c.Hash[:], p.Hash)
		cursors = append(cursors, c)
	}
	at, ok := s.chain.Intersect(x.peer, cursors)
	if !ok {
		return pcommon.Point{}, viewTip(s.chain, x), chainsync.ErrIntersectNotFound
	}
	x.cursor = at
	return pointOf(s.chain, at), viewTip(s.chain, x), nil
}

// decide is the next message for the peer at remote. With park, a peer with
// nothing to be sent is marked waiting.
func (s *shared) decide(remote net.Addr, srv *chainsync.Server, park bool) (reply, PeerID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, err := lookup(s.sessions, remote)
	if err != nil {
		return reply{}, "", err
	}
	x.server = srv
	r := nextReply(s.chain, x)
	if park && r.step.Move == Wait {
		x.waiting = true
	}
	return r, x.peer, nil
}

// waiters takes the replies every waiting peer is owed after a chain
// change, and the peers they go to.
func (s *shared) waiters() ([]reply, []PeerID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []reply
	var peers []PeerID
	for _, x := range s.sessions {
		if !x.waiting {
			continue
		}
		r := nextReply(s.chain, x)
		if r.step.Move == Wait {
			continue
		}
		x.waiting = false
		out, peers = append(out, r), append(peers, x.peer)
	}
	return out, peers
}

// blocks is the blocks from one hash to another on any fork known.
func (s *shared) blocks(from, to [32]byte) ([]Block, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chain.Range(from, to)
}

func lookup(sessions map[string]*session, remote net.Addr) (*session, error) {
	if remote == nil {
		return nil, errors.New("callback without a remote address")
	}
	s, ok := sessions[remote.String()]
	if !ok {
		return nil, fmt.Errorf("no session for %s", remote)
	}
	return s, nil
}
