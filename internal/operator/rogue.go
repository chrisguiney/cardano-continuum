package operator

import (
	"net"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
)

// Rogue renders continuum-forger, the block producer that breaks consensus
// rules on purpose (internal/forger). It is fenced like every other
// implementation: Render gives it the pool's one workload name, the
// finalizer and the keys gate.
//
// The forger reads its misbehaviours from behaviour.json in the keys
// Secret. The engine writes that Secret for one scenario, so the schedule
// cannot outlive the scenario, and a Secret without the entry runs an
// honest pool.
type Rogue struct{}

// rogueBinary is where ko puts the binary in the image, which has no shell
// and no other tool.
const rogueBinary = "/ko-app/continuum-forger"

// Impl is v1alpha1.Rogue.
func (Rogue) Impl() v1alpha1.Implementation { return v1alpha1.Rogue }

// Image is the built-in default image. A developer cluster overrides it
// with the image `make ko-images` pushes to its own registry.
func (Rogue) Image() string { return ident.ImageRepo + "/continuum-forger:latest" }

// DataDir is empty: the forger keeps the chain in memory.
func (Rogue) DataDir() string { return "" }

// Requests is sized for a small devnet.
func (Rogue) Requests() Demand { return Demand{CPUMillis: 100, MemoryMiB: 256} }

// Node renders the forger container. Its peers are the nodes whose chain it
// follows; an unkeyed node follows and serves the chain and forges nothing.
func (Rogue) Node(s NodeSpec) (Node, error) {
	args := []string{
		"run",
		"--genesis", GenesisDir,
		"--listen", ":" + strconv.Itoa(int(N2NPort)),
		"--evidence", DataMount + "/evidence",
	}
	for _, p := range s.Peers {
		args = append(args, "--peer", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	}
	if s.Keyed {
		args = append(args, "--keys", KeysDir)
	}
	return Node{
		Container: corev1.Container{Command: []string{rogueBinary}, Args: args},
		Config:    map[string]string{},
		// The forger checks its own keys: the image has no test(1). A blank
		// Secret fails the gate and keeps the pod pending.
		Gate: []string{rogueBinary, "gate", "--keys", KeysDir},
	}, nil
}
