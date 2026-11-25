package operator

import (
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

func TestPeersPolicy(t *testing.T) {
	members := []Member{
		{Name: "pool-a", PoolID: "a", Role: RoleForger},
		{Name: "relay-a1", PoolID: "a", Role: RoleRelay},
		{Name: "pool-b", PoolID: "b", Role: RoleForger},
		{Name: "relay-b1", PoolID: "b", Role: RoleRelay},
	}
	host := func(n string) Peer { return Peer{Host: n + ".ns.svc.cluster.local", Port: N2NPort} }
	tests := []struct {
		policy v1alpha1.TopologyMode
		self   int
		want   []Peer
	}{
		{v1alpha1.TopologyMesh, 0, []Peer{host("pool-b"), host("relay-a1"), host("relay-b1")}},
		{v1alpha1.TopologyRelayed, 0, []Peer{host("relay-a1")}},
		{v1alpha1.TopologyRelayed, 1, []Peer{host("pool-a"), host("relay-b1")}},
	}
	for _, tt := range tests {
		got := Peers(tt.policy, "ns", members, members[tt.self])
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s self %s: %v, want %v", tt.policy, members[tt.self].Name, got, tt.want)
		}
		if slices.Contains(got, host(members[tt.self].Name)) {
			t.Errorf("%s: peers include self", tt.policy)
		}
	}
}
