package operator

import (
	"encoding/json"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
)

// CardanoNode renders cardano-node. The node reads genesis.ConfigFile from
// the genesis ConfigMap, which names the per-era genesis files relative to
// itself, and a topology this driver renders from the peers.
type CardanoNode struct{}

const cardanoTopology = "topology.json"

// Impl is v1alpha1.CardanoNode.
func (CardanoNode) Impl() v1alpha1.Implementation { return v1alpha1.CardanoNode }

// Image is the built-in default image. Its entrypoint is bypassed; the
// binary is on PATH.
func (CardanoNode) Image() string { return "ghcr.io/blinklabs-io/cardano-node:11.1.3-2" }

// DataDir is the ImmutableDB under the node's database path.
func (CardanoNode) DataDir() string { return DataMount + "/db/immutable" }

// Requests is sized for a small devnet.
func (CardanoNode) Requests() Demand { return Demand{CPUMillis: 250, MemoryMiB: 1024} }

// Node renders the cardano-node container. A keyed node gets the three key
// flags; the engine issues the opcert, so the node never rotates KES itself.
func (CardanoNode) Node(s NodeSpec) (Node, error) {
	topo, err := cardanoTopologyJSON(s.Peers)
	if err != nil {
		return Node{}, err
	}
	args := []string{
		"run",
		"--config", GenesisDir + "/" + genesis.ConfigFile,
		"--topology", ConfigDir + "/" + cardanoTopology,
		"--database-path", DataMount + "/db",
		"--socket-path", DataMount + "/node.socket",
		"--host-addr", "0.0.0.0",
		"--port", strconv.Itoa(int(N2NPort)),
	}
	if s.Keyed {
		args = append(args,
			"--shelley-kes-key", KeysDir+"/"+KESKey,
			"--shelley-vrf-key", KeysDir+"/"+VRFKey,
			"--shelley-operational-certificate", KeysDir+"/"+OpCert,
		)
	}
	return Node{
		Container: corev1.Container{Command: []string{"cardano-node"}, Args: args},
		Config:    map[string]string{cardanoTopology: topo},
		// test(1) from coreutils, without a shell: a blank or missing opcert
		// keeps the pod in its init phase.
		Gate: []string{"test", "-s", KeysDir + "/" + OpCert},
		// socat ships in the node image. A unix socket cannot be shared
		// between pods, so the engine reaches this TCP bridge instead.
		NtC: []string{"socat", fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", NtCPort),
			"UNIX-CONNECT:" + DataMount + "/node.socket"},
	}, nil
}

type cardanoAccessPoint struct {
	Address string `json:"address"`
	Port    int32  `json:"port"`
}

type cardanoLocalRoot struct {
	AccessPoints []cardanoAccessPoint `json:"accessPoints"`
	Advertise    bool                 `json:"advertise"`
	Trustable    bool                 `json:"trustable"`
	Valency      int                  `json:"valency"`
}

// cardanoTopologyJSON makes every peer a trusted local root, with no public
// roots and no ledger peers, as internal/genesis does for genesis pools.
func cardanoTopologyJSON(peers []Peer) (string, error) {
	root := cardanoLocalRoot{AccessPoints: []cardanoAccessPoint{}, Trustable: true, Valency: len(peers)}
	for _, p := range peers {
		root.AccessPoints = append(root.AccessPoints, cardanoAccessPoint{p.Host, p.Port})
	}
	b, err := json.MarshalIndent(struct {
		LocalRoots         []cardanoLocalRoot `json:"localRoots"`
		PublicRoots        []struct{}         `json:"publicRoots"`
		UseLedgerAfterSlot int                `json:"useLedgerAfterSlot"`
	}{[]cardanoLocalRoot{root}, []struct{}{}, -1}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("render cardano-node topology: %w", err)
	}
	return string(b) + "\n", nil
}
