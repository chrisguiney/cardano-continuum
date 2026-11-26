package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContinuumNetworkSpec describes a simulated Cardano network.
type ContinuumNetworkSpec struct {
	// Magic is the network magic every node on this network uses.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	Magic uint32 `json:"magic"`

	// GenesisRef names the genesis ConfigMap the operator mounts into every
	// node on this network.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	GenesisRef string `json:"genesisRef"`

	// Topology controls which peers each pool dials.
	//
	// +kubebuilder:validation:Required
	Topology TopologyPolicy `json:"topology"`

	// Budget caps the total resources the network's pools may request.
	//
	// +kubebuilder:validation:Required
	Budget BudgetSpec `json:"budget"`

	// Images sets the default image per implementation. A ContinuumPool's
	// image overrides it. Unset implementations use the operator's built-in
	// default.
	//
	// +optional
	Images ImageSet `json:"images,omitempty"`

	// ImagePullSecrets names Secrets the operator attaches to every pod it
	// renders. The developer registry is open, so the default is none.
	//
	// +optional
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`

	// EpochLength is the number of slots per epoch.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	EpochLength int64 `json:"epochLength"`

	// SecurityParam is the Ouroboros security parameter k.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	SecurityParam int64 `json:"securityParam"`

	// Parquet adds a mithril2pq sidecar to every validating and forging pool
	// pod on the network.
	//
	// +optional
	Parquet ParquetSpec `json:"parquet,omitempty"`
}

// ParquetSpec configures the mithril2pq sidecar.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.enabled) && self.enabled) || (has(self.image) && size(self.image) > 0 && has(self.outputClaim) && size(self.outputClaim) > 0 && has(self.intervalSeconds) && self.intervalSeconds > 0)",message="enabled needs image, outputClaim and intervalSeconds"
type ParquetSpec struct {
	// Enabled adds the sidecar. It mounts the node's data volume read-only
	// and writes Parquet to OutputClaim.
	//
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Image runs the sidecar. It must provide /bin/sh and mithril2pq on PATH,
	// because the sidecar's loop is a shell script. The distroless
	// mithril2pq target has neither.
	//
	// +optional
	Image string `json:"image,omitempty"`

	// IntervalSeconds is the pause between conversion runs.
	//
	// +optional
	// +kubebuilder:validation:Minimum=1
	IntervalSeconds int32 `json:"intervalSeconds,omitempty"`

	// OutputClaim names the PersistentVolumeClaim mounted at /parquet in
	// every sidecar. Each pool writes to /parquet/<pool name>.
	//
	// +optional
	OutputClaim string `json:"outputClaim,omitempty"`
}

// ContinuumNetworkStatus is the observed state of a ContinuumNetwork.
type ContinuumNetworkStatus struct {
	// Phase is a coarse summary of the network's state.
	//
	// +optional
	Phase string `json:"phase,omitempty"`

	// GenesisHash is the hash of the genesis the network was started from.
	//
	// +optional
	GenesisHash string `json:"genesisHash,omitempty"`

	// ForgingStakeRatio is the fraction of stake held by forging pools.
	//
	// +optional
	ForgingStakeRatio string `json:"forgingStakeRatio,omitempty"`

	// Images holds the digest and commit each implementation resolved to for
	// this run. Dingo's entry carries its build provenance.
	//
	// +optional
	Images map[Implementation]ResolvedImage `json:"images,omitempty"`

	// Tips holds the last observed tip of each pool, keyed by pool name.
	//
	// +optional
	Tips map[string]TipStatus `json:"tips,omitempty"`

	// Conditions report the network's observed state.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ContinuumNetwork is a simulated Cardano network that ContinuumPools join.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type ContinuumNetwork struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContinuumNetworkSpec   `json:"spec,omitempty"`
	Status ContinuumNetworkStatus `json:"status,omitempty"`
}

// ContinuumNetworkList is a list of ContinuumNetwork objects.
//
// +kubebuilder:object:root=true
type ContinuumNetworkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContinuumNetwork `json:"items"`
}
