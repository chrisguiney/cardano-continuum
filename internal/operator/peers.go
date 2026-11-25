package operator

import (
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// Member is a pool of a network that renders a workload, as Peers sees it.
type Member struct {
	Name   string // workload name
	PoolID string
	Role   Role
}

// Peers is who self dials under policy, sorted by host and never self.
// Under mesh every node dials every other. Under relayed a keyed node dials
// only the relays of its own PoolID, and a relay dials its own pool's keyed
// nodes and every other relay.
func Peers(policy v1alpha1.TopologyMode, namespace string, members []Member, self Member) []Peer {
	var out []Peer
	for _, m := range members {
		if m.Name == self.Name || !dials(policy, self, m) {
			continue
		}
		out = append(out, Peer{Host: m.Name + "." + namespace + ".svc", Port: N2NPort})
	}
	slices.SortFunc(out, func(a, b Peer) int { return strings.Compare(a.Host, b.Host) })
	return out
}

func dials(policy v1alpha1.TopologyMode, self, m Member) bool {
	if policy != v1alpha1.TopologyRelayed {
		return true
	}
	if self.Role.Keyed() {
		return m.Role == RoleRelay && m.PoolID == self.PoolID
	}
	return m.Role == RoleRelay || m.PoolID == self.PoolID
}
