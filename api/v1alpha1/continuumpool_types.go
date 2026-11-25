package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContinuumPoolSpec describes one simulated stake pool.
type ContinuumPoolSpec struct {
	// NetworkRef names the ContinuumNetwork this pool joins.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	NetworkRef string `json:"networkRef"`

	// PoolID is the pool's identifier. The fence admits at most one forging
	// pool per PoolID, across every implementation.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	PoolID string `json:"poolId"`

	// Implementation selects the node implementation.
	//
	// +kubebuilder:validation:Required
	Implementation Implementation `json:"impl"`

	// Tier selects what the pool renders.
	//
	// +kubebuilder:validation:Required
	Tier Tier `json:"tier"`

	// KeysSecret names the Secret holding kes.skey, vrf.skey and
	// opcert.cert. The engine writes it. Only forging-tier pools mount it.
	//
	// +optional
	KeysSecret string `json:"keysSecret,omitempty"`

	// Image overrides the network's default image for this implementation.
	//
	// +optional
	Image string `json:"image,omitempty"`

	// Dark scales the workload to zero. The pool stays registered and misses
	// blocks.
	//
	// +optional
	Dark bool `json:"dark,omitempty"`

	// Analytics adds the mithril2pq sidecar, which writes the node's
	// ImmutableDB to the run's Parquet volume. Only implementations with an
	// ImmutableDB support it.
	//
	// +optional
	Analytics bool `json:"analytics,omitempty"`

	// UnsafeDuplicateOf names another pool whose PoolID this pool forges
	// with on purpose. It is admitted only while the network carries the
	// scenario annotation and this pool is labelled with that scenario id.
	// The pool is deleted when the scenario ends. It permits two forgers for
	// one PoolID, which is equivocation. Use it only in scenarios that test
	// the fence or a fork.
	//
	// +optional
	UnsafeDuplicateOf string `json:"unsafeDuplicateOf,omitempty"`
}

// ContinuumPoolStatus is the observed state of a ContinuumPool.
type ContinuumPoolStatus struct {
	// Phase is the observed lifecycle phase.
	//
	// +optional
	Phase PoolPhase `json:"phase,omitempty"`

	// Tip is the pool node's last observed chain tip.
	//
	// +optional
	Tip TipStatus `json:"tip,omitempty"`

	// Workload names the StatefulSet the operator rendered for this pool.
	//
	// +optional
	Workload string `json:"workload,omitempty"`

	// Image is the image the workload runs, pinned to its digest.
	//
	// +optional
	Image ResolvedImage `json:"image,omitempty"`

	// Build is the build provenance of the running image.
	//
	// +optional
	Build BuildInfo `json:"build,omitempty"`

	// LastRoll records the most recent change of the workload's image
	// digest. A first start is not a roll.
	//
	// +optional
	LastRoll *RollStatus `json:"lastRoll,omitempty"`

	// Conditions report admission, fencing and budget decisions.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RollStatus is one image roll: the pod ran From, then To.
type RollStatus struct {
	// From is the digest the pool ran before the roll.
	From string `json:"from"`

	// To is the digest the pool runs after it.
	To string `json:"to"`

	// Time is when the operator changed the workload.
	Time metav1.Time `json:"time"`
}

// ContinuumPool is one simulated stake pool on a ContinuumNetwork.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Impl",type=string,JSONPath=`.spec.impl`
// +kubebuilder:printcolumn:name="Tier",type=string,JSONPath=`.spec.tier`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Tip Slot",type=integer,JSONPath=`.status.tip.slot`
type ContinuumPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContinuumPoolSpec   `json:"spec,omitempty"`
	Status ContinuumPoolStatus `json:"status,omitempty"`
}

// ContinuumPoolList is a list of ContinuumPool objects.
//
// +kubebuilder:object:root=true
type ContinuumPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContinuumPool `json:"items"`
}
