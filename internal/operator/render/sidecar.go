package render

import (
	"cmp"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// ParquetDir is where the sidecar writes. Each pool writes to a directory of
// its own name under it, all on one claim.
const ParquetDir = "/parquet"

const (
	volParquet    = "parquet"
	sidecarName   = "mithril2pq"
	sidecarBin    = "/usr/local/bin/mithril2pq"
	sidecarCPU    = 50
	sidecarMemMiB = 128
)

// Sidecar configures the mithril2pq container of one pool.
type Sidecar struct {
	// Image runs the sidecar.
	Image string
	// Pool is the ContinuumPool name, the last element of the output path.
	Pool string
	// Magic is the network magic the sidecar passes to mithril2pq.
	Magic uint32
	// IntervalSeconds is the pause between conversion passes, passed as -watch.
	IntervalSeconds int32
	// OutputClaim is the PersistentVolumeClaim mounted at ParquetDir.
	OutputClaim string
}

// WithSidecar returns w with the mithril2pq sidecar added to its pod. The
// sidecar reads the driver's ImmutableDB through the node's data volume,
// mounted read-only, so it cannot write into the node's storage. A logical
// workload renders nothing and is returned unchanged.
//
// mithril2pq runs with -watch, so it loops itself and needs no shell: the
// distroless image has none. A failed pass is retried by mithril2pq; a
// configuration error exits and the kubelet restarts the container.
func WithSidecar(w Workload, d NodeDriver, s Sidecar) (Workload, error) {
	if w.StatefulSet == nil {
		return w, nil
	}
	dir := d.DataDir()
	if dir == "" {
		return Workload{}, fmt.Errorf("mithril2pq sidecar for %s: driver has no ImmutableDB", d.Impl())
	}
	out := ParquetDir + "/" + s.Pool
	args := []string{
		"-watch", fmt.Sprintf("%ds", s.IntervalSeconds),
		"-immutable-dir", dir,
		"-network-magic", fmt.Sprint(s.Magic),
		"-out", out,
	}
	c := corev1.Container{
		Name:      sidecarName,
		Image:     s.Image,
		Command:   append([]string{sidecarBin}, args...),
		Resources: requests(Demand{CPUMillis: sidecarCPU, MemoryMiB: sidecarMemMiB}),
		VolumeMounts: []corev1.VolumeMount{
			{Name: volData, MountPath: DataMount, ReadOnly: true},
			{Name: volParquet, MountPath: ParquetDir},
		},
	}
	set := w.StatefulSet.DeepCopy()
	set.Spec.Template.Spec.Containers = append(set.Spec.Template.Spec.Containers, c)
	set.Spec.Template.Spec.Volumes = append(set.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: volParquet,
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: s.OutputClaim,
		}},
	})
	w.StatefulSet = set
	return w, nil
}

// PoolDemand is what one pool takes from the budget: its node and, for the
// network's source pool, the sidecar.
func PoolDemand(d NodeDriver, source bool) Demand {
	dem := d.Requests()
	if source {
		dem.CPUMillis += sidecarCPU
		dem.MemoryMiB += sidecarMemMiB
	}
	return dem
}

// ParquetSource names the one pool of network whose pod carries the
// sidecar, or "" when the sidecar is off or no pool qualifies. pools may
// hold other networks' pools and ones that render nothing; they are skipped.
//
// With spec.Pool set, the source is the lowest-named pool with that PoolID.
// Otherwise it is the lowest-named relay, else the lowest-named keyed pool.
// Names alone decide, so every reconciler that sees the same pools agrees.
func ParquetSource(network string, spec v1alpha1.ParquetSpec, pools []v1alpha1.ContinuumPool) string {
	if !spec.Enabled {
		return ""
	}
	var relay, keyed, named string
	for i := range pools {
		p := &pools[i]
		role := RoleOf(p)
		if p.Spec.NetworkRef != network || role == RoleNone || p.DeletionTimestamp != nil {
			continue
		}
		switch {
		case spec.Pool != "":
			if p.Spec.PoolID == spec.Pool {
				named = lower(named, p.Name)
			}
		case role == RoleRelay:
			relay = lower(relay, p.Name)
		default:
			keyed = lower(keyed, p.Name)
		}
	}
	return cmp.Or(named, relay, keyed)
}

// lower is the smaller of a and b, where "" is unset.
func lower(a, b string) string {
	if a == "" || b < a {
		return b
	}
	return a
}
