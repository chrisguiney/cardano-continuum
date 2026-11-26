package v1alpha1

// Implementation names the node implementation a ContinuumPool runs.
//
// +kubebuilder:validation:Enum=dingo;cardano-node;amaru;rogue
type Implementation string

const (
	// Dingo is the dingo node. Its ImmutableDB is unreleased, so dingo
	// pods track origin/main pinned to a digest per run.
	Dingo Implementation = "dingo"

	// CardanoNode is the cardano-node implementation.
	CardanoNode Implementation = "cardano-node"

	// Amaru is accepted by the type but unsupported: the Amaru driver is
	// blocked because amaru cannot join a blank devnet. See
	// docs/spikes/amaru-devnet.md. The operator must refuse it until that
	// is resolved.
	Amaru Implementation = "amaru"

	// Rogue is the continuum-forger, a deliberately misbehaving forger.
	Rogue Implementation = "rogue"
)

// Tier sets what a ContinuumPool renders.
//
// +kubebuilder:validation:Enum=logical;validating;forging
type Tier string

const (
	// TierLogical renders nothing: the pool exists only as certificates.
	TierLogical Tier = "logical"

	// TierValidating renders a relay pod with no keys mounted.
	TierValidating Tier = "validating"

	// TierForging renders a block-producer pod with the KES, VRF and opcert
	// Secret mounted.
	TierForging Tier = "forging"
)

// PoolPhase is the observed lifecycle phase of a ContinuumPool.
//
// +kubebuilder:validation:Enum=Pending;Syncing;Forging;Dark
type PoolPhase string

const (
	// PhasePending means no pod has been observed yet.
	PhasePending PoolPhase = "Pending"

	// PhaseSyncing means a pod is ready but its tip is behind.
	PhaseSyncing PoolPhase = "Syncing"

	// PhaseForging means a forging-tier pool is at the current tip.
	PhaseForging PoolPhase = "Forging"

	// PhaseDark means the pool is scaled to zero on purpose and misses blocks.
	PhaseDark PoolPhase = "Dark"
)

// BuildMode records how an image's binaries were produced.
//
// +kubebuilder:validation:Enum=upstream;fast;repro
type BuildMode string

const (
	// BuildModeUpstream is a published CI image.
	BuildModeUpstream BuildMode = "upstream"

	// BuildModeFast is a developer build that reuses cached layers.
	BuildModeFast BuildMode = "fast"

	// BuildModeRepro is a developer build that is reproducible from its commit.
	BuildModeRepro BuildMode = "repro"
)

// TopologyMode selects which peers a pool dials.
//
// +kubebuilder:validation:Enum=relayed;mesh
type TopologyMode string

const (
	// TopologyRelayed makes a forging pool peer only with its own relays.
	TopologyRelayed TopologyMode = "relayed"

	// TopologyMesh makes every pool peer with every other pool.
	TopologyMesh TopologyMode = "mesh"
)

// TopologyPolicy controls the peer graph the operator renders.
type TopologyPolicy struct {
	// Policy selects the peer graph shape.
	//
	// +kubebuilder:validation:Required
	Policy TopologyMode `json:"policy"`
}

// The default budget of a ContinuumNetwork that sets none: sixteen pools
// requesting the drivers' 250m and 1 GiB each. It fits a 48 GiB machine with
// room to spare; docs/single-machine.md has the measurements behind it. The
// kubebuilder default markers on BudgetSpec repeat these values, and
// TestBudgetDefaults fails if they drift.
const (
	DefaultMaxPools  = 16
	DefaultCPUMillis = 4000
	DefaultMemoryMiB = 16384
)

// BudgetSpec caps the resources a network's pools may request in total.
// The operator refuses an over-budget pool rather than scheduling it.
type BudgetSpec struct {
	// MaxPools is the largest number of pool workloads the network may run.
	//
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=16
	MaxPools int32 `json:"maxPools"`

	// CPUMillis is the total CPU request budget in millicores.
	//
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=4000
	CPUMillis int64 `json:"cpuMillis"`

	// MemoryMiB is the total memory request budget in mebibytes.
	//
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=16384
	MemoryMiB int64 `json:"memoryMiB"`
}

// ImageSet holds a default image reference per implementation. A reference
// is a tag or a digest. A pool's own image overrides it.
type ImageSet map[Implementation]string

// ResolvedImage is an image reference pinned to the digest it resolved to.
type ResolvedImage struct {
	// Ref is the reference as configured.
	//
	// +optional
	Ref string `json:"ref,omitempty"`

	// Digest is the manifest digest the reference resolved to. Pods run
	// image@digest, so a moved tag rolls only pods whose digest changed.
	//
	// +optional
	Digest string `json:"digest,omitempty"`

	// Commit is the source commit, from the OCI revision label or the image
	// ENV COMMIT_HASH. Empty for third-party images that carry neither.
	//
	// +optional
	Commit string `json:"commit,omitempty"`

	// Build describes how the image was produced.
	//
	// +optional
	Build BuildInfo `json:"build,omitempty"`
}

// BuildInfo is the provenance of a developer or upstream build.
type BuildInfo struct {
	// Mode is how the build was produced.
	//
	// +optional
	Mode BuildMode `json:"mode,omitempty"`

	// Dirty is true when the build included uncommitted source. A finding
	// from a dirty build is not reproducible from its commit.
	//
	// +optional
	Dirty bool `json:"dirty,omitempty"`

	// Revisions maps each dependency group (for example dingo, gouroboros,
	// plutigo, ouroboros-mock) to the revision it was built from.
	//
	// +optional
	Revisions map[string]string `json:"revisions,omitempty"`
}

// TipStatus is a node's chain tip as last observed by the operator.
type TipStatus struct {
	// Slot is the tip slot.
	//
	// +optional
	Slot int64 `json:"slot,omitempty"`

	// Hash is the tip block hash, hex encoded.
	//
	// +optional
	Hash string `json:"hash,omitempty"`
}
