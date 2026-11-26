package render

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
)

// Labels, annotations and the finalizer the operator puts on what it renders.
const (
	// LabelPool is PoolKey of the pool's PoolID. Every pod holding or able to
	// hold the pool's keys carries it, the scenario duplicate included, so
	// one label selector lists them all.
	LabelPool = ident.LabelPrefix + "/pool"
	// LabelRole is the Role of the workload.
	LabelRole = ident.LabelPrefix + "/role"
	// LabelImpl is the implementation the workload runs.
	LabelImpl = ident.LabelPrefix + "/impl"
	// LabelWorkload is the workload name; it is the StatefulSet selector.
	LabelWorkload = ident.LabelPrefix + "/workload"
	// LabelOwner is the UID of the ContinuumPool that rendered the object.
	LabelOwner = ident.LabelPrefix + "/owner"
	// AnnotationPoolName is the name of the ContinuumPool that rendered the
	// object. It is an annotation because a resource name can exceed the
	// 63 characters a label value allows.
	AnnotationPoolName = ident.LabelPrefix + "/pool-name"
	// AnnotationLoaded on a pod records the Config its node container has
	// reloaded, as "<container instance> <config digest>".
	AnnotationLoaded = ident.LabelPrefix + "/config-loaded"
	// Finalizer holds a keyed pod object until its process is known to have
	// stopped. See Releasable.
	Finalizer = ident.LabelPrefix + "/fence"
)

// Role is what a pool's workload is for.
type Role string

// The roles. RoleNone renders nothing.
const (
	RoleNone      Role = ""
	RoleRelay     Role = "relay"
	RoleForger    Role = "forger"
	RoleDuplicate Role = "duplicate"
)

// Keyed reports whether the role mounts a keys Secret.
func (r Role) Keyed() bool { return r == RoleForger || r == RoleDuplicate }

// RoleOf is the role p's spec asks for: a logical pool renders nothing, a
// validating pool a relay, and a forging pool the keyed forger, or the
// scenario's duplicate when UnsafeDuplicateOf is set.
func RoleOf(p *v1alpha1.ContinuumPool) Role {
	switch p.Spec.Tier {
	case v1alpha1.TierForging:
		if p.Spec.UnsafeDuplicateOf != "" {
			return RoleDuplicate
		}
		return RoleForger
	case v1alpha1.TierValidating:
		return RoleRelay
	}
	return RoleNone
}

// PoolKey is a label-safe digest of a PoolID: 16 hex characters.
func PoolKey(poolID string) string { return digest(poolID) }

// Name is the StatefulSet, Service and pod-name stem of p's workload, or ""
// for a pool that renders nothing.
//
// The forger's name depends on the PoolID alone: not the implementation,
// image, generation or resource name. That name is the fence. Two keyed
// workloads for one PoolID would need two StatefulSets of the same name, and
// the API server refuses the second create, whichever driver, digest or
// reconciler asked for it (models/specs/pool-forging, contract rule 1). The
// fence is per namespace; a network and its pools share one.
func Name(p *v1alpha1.ContinuumPool) string {
	switch RoleOf(p) {
	case RoleForger:
		return "pool-" + PoolKey(p.Spec.PoolID)
	case RoleDuplicate:
		return "dup-" + digest(p.Spec.PoolID+"\x00"+p.Name)
	case RoleRelay:
		return "relay-" + digest(p.Name)
	}
	return ""
}

// PodName is the name of the single pod of StatefulSet set.
func PodName(set string) string { return set + "-0" }

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}
