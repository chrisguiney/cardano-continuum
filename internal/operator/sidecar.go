package operator

import (
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// ParquetDir is where the sidecar writes. Each pool writes to a directory of
// its own name under it, all on one claim.
const ParquetDir = "/parquet"

const (
	volParquet    = "parquet"
	sidecarName   = "mithril2pq"
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
	// IntervalSeconds is the pause between conversion runs.
	IntervalSeconds int32
	// OutputClaim is the PersistentVolumeClaim mounted at ParquetDir.
	OutputClaim string
}

// WithSidecar returns w with the mithril2pq sidecar added to its pod. The
// sidecar reads the driver's ImmutableDB through the node's data volume,
// mounted read-only, so it cannot write into the node's storage. A logical
// workload renders nothing and is returned unchanged.
//
// The loop is a shell script, so the image must have /bin/sh. Each pass
// creates the output directory, converts the finished chunks, and sleeps.
// A failed pass is not fatal: the next pass retries it.
func WithSidecar(w Workload, d NodeDriver, s Sidecar) (Workload, error) {
	if w.StatefulSet == nil {
		return w, nil
	}
	dir := d.DataDir()
	if dir == "" {
		return Workload{}, fmt.Errorf("mithril2pq sidecar for %s: driver has no ImmutableDB", d.Impl())
	}
	out := ParquetDir + "/" + s.Pool
	script := fmt.Sprintf("while :; do mkdir -p %[1]s && mithril2pq -immutable-dir %[2]s -network-magic %[3]d -out %[1]s; sleep %[4]d; done",
		shQuote(out), shQuote(dir), s.Magic, s.IntervalSeconds)
	c := corev1.Container{
		Name:      sidecarName,
		Image:     s.Image,
		Command:   []string{"sh", "-c", script},
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

var shSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=+@-]+$`)

// shQuote quotes a value for the sh script. Pool names are Kubernetes object
// names and may hold characters the shell would expand.
func shQuote(v string) string {
	if shSafe.MatchString(v) {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}
