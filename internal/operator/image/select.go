// Package image chooses the image for each node, pins it to a digest and
// decides whether a change needs a roll. Selection, pull policy and roll
// detection are pure; Resolver is the only code that touches a registry.
package image

import (
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// Source says which level of the precedence chain supplied an image.
type Source int

// The precedence chain, highest first.
const (
	// FromPool is ContinuumPool.spec.image.
	FromPool Source = iota
	// FromNetwork is ContinuumNetwork.spec.images[impl].
	FromNetwork
	// BuiltIn is the operator built-in default. It is also the source
	// reported when no level names the implementation.
	BuiltIn
)

// Select returns the image reference for impl: the pool's own image, else the
// network default, else the operator built-in default. The reference is
// empty when no level names impl; the caller must refuse to render it.
func Select(def, net v1alpha1.ImageSet, impl v1alpha1.Implementation, pool string) (string, Source) {
	if pool != "" {
		return pool, FromPool
	}
	if ref := net[impl]; ref != "" {
		return ref, FromNetwork
	}
	return def[impl], BuiltIn
}

// PullPolicy is IfNotPresent for a reference pinned by digest and Always for a
// mutable tag, which may have moved since the node last pulled it.
func PullPolicy(ref string) corev1.PullPolicy {
	if strings.Contains(ref, "@sha256:") {
		return corev1.PullIfNotPresent
	}
	return corev1.PullAlways
}

// NeedsRoll reports whether next must replace cur. Only a changed digest
// rolls: a new tag over the same digest changes nothing a node runs, and an
// unresolved next must never roll a healthy pod. A pod with no recorded
// digest (first resolution) rolls to the resolved one.
func NeedsRoll(cur, next v1alpha1.ResolvedImage) bool {
	if next.Digest == "" {
		return false
	}
	return cur.Digest != next.Digest
}
