package render

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
)

// Dingo renders dingo from its main branch (decision D13): main writes an
// ImmutableDB, no release does yet. The node reads the network's
// genesis.ConfigFile from the genesis ConfigMap, as cardano-node does, plus a
// dingo config file and topology this driver renders.
//
// The config keys are those of dingo's internal/config/config.go on main,
// which decodes the file with unknown fields refused.
type Dingo struct{}

const (
	DingoConfigFile = "dingo.yaml"
	dingoTopology   = "topology.json"
	// dingoNtCPort serves node-to-client over TCP on the loopback address,
	// for a sidecar in the pod.
	dingoNtCPort = 3002
	// dingoSocket is under /tmp because dingo refuses a socket directory
	// writable by others unless it is sticky: the data emptyDir is 0777, and
	// the image's /ipc belongs to a user the node does not run as.
	dingoSocket = "/tmp/node.socket"
)

// Impl is v1alpha1.Dingo.
func (Dingo) Impl() v1alpha1.Implementation { return v1alpha1.Dingo }

// Image is the built-in default image, dingo's main branch. The run pins it
// to a digest (internal/operator/image). Its entrypoint is bypassed; the
// binary is on PATH.
func (Dingo) Image() string { return "ghcr.io/blinklabs-io/dingo:main" }

// DataDir is the ImmutableDB dingo keeps under its database path
// (database/plugin/immutable DirName).
func (Dingo) DataDir() string { return DataMount + "/db/immutable" }

// Requests is sized for a small devnet.
func (Dingo) Requests() Demand { return Demand{CPUMillis: 250, MemoryMiB: 1024} }

// dingoConfig is the subset of dingo's config file the driver sets. The JSON
// names are dingo's YAML keys.
type dingoConfig struct {
	Network         string `json:"network"`
	NetworkMagic    uint32 `json:"networkMagic"`
	CardanoConfig   string `json:"cardanoConfig"`
	Topology        string `json:"topology"`
	DatabasePath    string `json:"databasePath"`
	SocketPath      string `json:"socketPath"`
	BindAddr        string `json:"bindAddr"`
	RelayPort       int32  `json:"relayPort"`
	PrivateBindAddr string `json:"privateBindAddr"`
	PrivatePort     int32  `json:"privatePort"`
	BlockProducer   bool   `json:"blockProducer"`
	// One key set per node. Never shelleyKesAgentSocket as well: dingo's
	// validate.go refuses both, and the engine issues the opcert, so no
	// agent rotates KES.
	ShelleyVRFKey string `json:"shelleyVrfKey,omitempty"`
	ShelleyKESKey string `json:"shelleyKesKey,omitempty"`
	ShelleyOpCert string `json:"shelleyOperationalCertificate,omitempty"`
}

// Node renders the dingo container. A keyed node is a block producer whose
// key paths are the Secret mount.
func (Dingo) Node(s NodeSpec) (Node, error) {
	topo, err := cardanoTopologyJSON(s.Peers)
	if err != nil {
		return Node{}, err
	}
	cfg := dingoConfig{
		// "devnet" selects no built-in genesis or bootstrap peers; the
		// magic below and the genesis files decide the network.
		Network:         "devnet",
		NetworkMagic:    s.Magic,
		CardanoConfig:   GenesisDir + "/" + genesis.ConfigFile,
		Topology:        ConfigDir + "/" + dingoTopology,
		DatabasePath:    DataMount + "/db",
		SocketPath:      dingoSocket,
		BindAddr:        "0.0.0.0",
		RelayPort:       N2NPort,
		PrivateBindAddr: "127.0.0.1",
		PrivatePort:     dingoNtCPort,
		BlockProducer:   s.Keyed,
	}
	if s.Keyed {
		cfg.ShelleyVRFKey = KeysDir + "/" + VRFKey
		cfg.ShelleyKESKey = KeysDir + "/" + KESKey
		cfg.ShelleyOpCert = KeysDir + "/" + OpCert
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return Node{}, fmt.Errorf("render dingo config: %w", err)
	}
	return Node{
		Container: corev1.Container{
			Command: []string{"dingo"},
			Args:    []string{"--config", ConfigDir + "/" + DingoConfigFile, "serve"},
			Env:     dingoEnv(cfg),
			// The keys Secret is root-owned at 0400, and dingo's keystore
			// refuses a key file with any group or other access, so an
			// fsGroup (which adds group read) cannot replace running as
			// root.
			SecurityContext: &corev1.SecurityContext{RunAsUser: ptr.To(int64(0)), RunAsGroup: ptr.To(int64(0))},
		},
		Config: map[string]string{DingoConfigFile: string(b), dingoTopology: topo},
		Gate:   []string{"test", "-s", KeysDir + "/" + OpCert},
	}, nil
}

// dingoEnv sets the variables the dingo image bakes in to the config's own
// values. dingo applies its environment over the config file, so the image's
// CARDANO_NETWORK=preview and its socket path would otherwise win.
func dingoEnv(c dingoConfig) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "CARDANO_NETWORK", Value: c.Network},
		{Name: "CARDANO_DATABASE_PATH", Value: c.DatabasePath},
		{Name: "CARDANO_SOCKET_PATH", Value: c.SocketPath},
	}
}
