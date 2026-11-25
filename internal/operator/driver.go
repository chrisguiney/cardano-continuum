package operator

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// Paths inside every node container. Render mounts them; drivers refer to
// them in their flags or config.
const (
	// GenesisDir holds the network's genesis ConfigMap: the node config and
	// the per-era genesis files, under the names internal/genesis gives them.
	GenesisDir = "/genesis"
	// ConfigDir holds the per-pool ConfigMap a driver renders (Node.Config).
	ConfigDir = "/config"
	// KeysDir holds the pool's keys Secret, read-only, mode 0400.
	KeysDir = "/keys"
	// DataMount is the node's writable data volume.
	DataMount = "/data"
)

// Entries of the keys Secret. The engine writes them as text envelopes; the
// operator only mounts the Secret.
const (
	KESKey = "kes.skey"
	VRFKey = "vrf.skey"
	OpCert = "opcert.cert"
)

// N2NPort is the node-to-node port of every node.
const N2NPort int32 = 3001

// Peer is a node another node dials.
type Peer struct {
	Host string
	Port int32
}

// NodeSpec is what a driver needs to render one node.
type NodeSpec struct {
	// Magic is the network magic.
	Magic uint32
	// Peers are the nodes this node dials.
	Peers []Peer
	// Keyed is true when the keys Secret is mounted at KeysDir.
	Keyed bool
}

// Node is a driver's part of a workload. Render adds the name, labels,
// image, resources, volumes and the fence.
type Node struct {
	// Container runs the node. Render sets its name, image, resources, ports
	// and volume mounts.
	Container corev1.Container
	// Config is the per-pool ConfigMap data, mounted at ConfigDir.
	Config map[string]string
	// Gate is the command of the init container of a keyed node. It exits 0
	// only when the keys at KeysDir can be used. A blank Secret (the engine
	// blanks it before every issue and retirement) keeps the pod pending.
	Gate []string
}

// Demand is the resources one node requests.
type Demand struct {
	CPUMillis int64
	MemoryMiB int64
}

// NodeDriver renders one node implementation. Implementations are pure.
type NodeDriver interface {
	// Impl is the implementation the driver renders.
	Impl() v1alpha1.Implementation
	// Image is the built-in default image reference.
	Image() string
	// Node renders the node for s.
	Node(s NodeSpec) (Node, error)
	// DataDir is the node's ImmutableDB path, or "" if it has none.
	DataDir() string
	// Requests is what one node requests; the budget counts it.
	Requests() Demand
}

// ErrUnsupported is returned for an implementation no driver renders.
var ErrUnsupported = errors.New("unsupported implementation")

// Registry maps an implementation to its driver.
type Registry map[v1alpha1.Implementation]NodeDriver

// Drivers is the registry of the drivers this build supports.
func Drivers() Registry {
	return Registry{v1alpha1.CardanoNode: CardanoNode{}}
}

// Driver returns the driver for impl. Amaru is refused even if registered:
// it cannot join a blank devnet (docs/spikes/amaru-devnet.md).
func (r Registry) Driver(impl v1alpha1.Implementation) (NodeDriver, error) {
	if impl == v1alpha1.Amaru {
		return nil, fmt.Errorf("%w: amaru cannot join a blank devnet (docs/spikes/amaru-devnet.md)", ErrUnsupported)
	}
	d, ok := r[impl]
	if !ok {
		return nil, fmt.Errorf("%w: no driver for %s", ErrUnsupported, impl)
	}
	return d, nil
}

// Defaults is the built-in default image of every registered driver.
func (r Registry) Defaults() v1alpha1.ImageSet {
	out := make(v1alpha1.ImageSet, len(r))
	for impl, d := range r {
		out[impl] = d.Image()
	}
	return out
}
