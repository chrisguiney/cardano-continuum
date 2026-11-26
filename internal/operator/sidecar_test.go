package operator

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// withParquet is network("ns") with the sidecar enabled.
func withParquet() *v1alpha1.ContinuumNetwork {
	n := network("ns")
	n.Spec.Parquet = v1alpha1.ParquetSpec{Enabled: true, Image: "mithril2pq:test", IntervalSeconds: 60, OutputClaim: "parquet"}
	return n
}

func renderOn(t *testing.T, d NodeDriver, n *v1alpha1.ContinuumNetwork, p *v1alpha1.ContinuumPool) Workload {
	t.Helper()
	w, err := Render(d, Input{
		Network: n,
		Pool:    p,
		Peers:   []Peer{{Host: "pool-b.ns.svc.cluster.local", Port: N2NPort}},
		Image:   Image{Name: "r@sha256:aaaa", PullPolicy: corev1.PullIfNotPresent},
	})
	if err != nil {
		t.Fatalf("Render %s: %v", d.Impl(), err)
	}
	return w
}

// sidecarOf returns the mithril2pq container of the pod, or false.
func sidecarOf(spec corev1.PodSpec) (corev1.Container, bool) {
	i := slices.IndexFunc(spec.Containers, func(c corev1.Container) bool { return c.Name == "mithril2pq" })
	if i < 0 {
		return corev1.Container{}, false
	}
	return spec.Containers[i], true
}

// TestParquetDisabledIsNoop: with the sidecar disabled, the rendered
// workload is the same whatever the other sidecar fields say.
func TestParquetDisabledIsNoop(t *testing.T) {
	for _, impl := range []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.Dingo} {
		for _, tier := range []v1alpha1.Tier{v1alpha1.TierValidating, v1alpha1.TierForging} {
			p := pool("ns", "a", impl, tier)
			plain := renderOn(t, Drivers()[impl], network("ns"), p)
			off := network("ns")
			off.Spec.Parquet = v1alpha1.ParquetSpec{Image: "mithril2pq:test", IntervalSeconds: 60, OutputClaim: "parquet"}
			got := renderOn(t, Drivers()[impl], off, p)
			a, _ := yaml.Marshal(plain)
			b, _ := yaml.Marshal(got)
			if string(a) != string(b) {
				t.Errorf("%s %s: disabled sidecar changed the workload", impl, tier)
			}
		}
	}
}

// TestSidecarGoldens: the workloads with the sidecar, for both drivers,
// relay and forging. The goldens without it stay as they were.
func TestSidecarGoldens(t *testing.T) {
	for _, impl := range []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.Dingo} {
		for _, tier := range []v1alpha1.Tier{v1alpha1.TierValidating, v1alpha1.TierForging} {
			name := string(impl) + "-" + map[v1alpha1.Tier]string{v1alpha1.TierValidating: "relay", v1alpha1.TierForging: "forging"}[tier] + "-parquet"
			w := renderOn(t, Drivers()[impl], withParquet(), pool("ns", "a", impl, tier))
			golden(t, name, w)
		}
	}
}

// TestSidecarMountsDataReadOnly: the sidecar reads the node's ImmutableDB
// from the node's data volume and cannot write to it. Its only writable
// mount is the output claim.
func TestSidecarMountsDataReadOnly(t *testing.T) {
	for _, impl := range []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.Dingo} {
		w := renderOn(t, Drivers()[impl], withParquet(), pool("ns", "a", impl, v1alpha1.TierForging))
		spec := w.StatefulSet.Spec.Template.Spec
		sc, ok := sidecarOf(spec)
		if !ok {
			t.Fatalf("%s: no mithril2pq container", impl)
		}
		var data, out *corev1.VolumeMount
		for i := range sc.VolumeMounts {
			m := &sc.VolumeMounts[i]
			switch m.MountPath {
			case DataMount:
				data = m
			case "/parquet":
				out = m
			}
		}
		if data == nil || data.Name != volData || !data.ReadOnly {
			t.Errorf("%s: data mount %+v, want %s read-only", impl, data, volData)
		}
		if out == nil || out.ReadOnly {
			t.Errorf("%s: output mount %+v, want writable /parquet", impl, out)
		}
		for _, m := range sc.VolumeMounts {
			if m.Name == volData && !m.ReadOnly {
				t.Errorf("%s: node data volume mounted writable in the sidecar", impl)
			}
		}
		i := slices.IndexFunc(spec.Volumes, func(v corev1.Volume) bool { return v.Name == out.Name })
		if i < 0 || spec.Volumes[i].PersistentVolumeClaim == nil || spec.Volumes[i].PersistentVolumeClaim.ClaimName != "parquet" {
			t.Errorf("%s: output volume is not the claim parquet", impl)
		}
		base := renderOn(t, Drivers()[impl], network("ns"), pool("ns", "a", impl, v1alpha1.TierForging))
		if got, want := len(spec.Containers), len(base.StatefulSet.Spec.Template.Spec.Containers)+1; got != want {
			t.Errorf("%s: %d containers, want %d (the driver's own plus the sidecar)", impl, got, want)
		}
	}
}

// TestSidecarCommand: the loop passes the driver's ImmutableDB, the network
// magic and a per-pool output directory to mithril2pq.
func TestSidecarCommand(t *testing.T) {
	for _, impl := range []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.Dingo} {
		p := pool("ns", "a", impl, v1alpha1.TierValidating)
		w := renderOn(t, Drivers()[impl], withParquet(), p)
		sc, _ := sidecarOf(w.StatefulSet.Spec.Template.Spec)
		cmd := strings.Join(append(sc.Command, sc.Args...), " ")
		for _, want := range []string{
			"mithril2pq -immutable-dir " + Drivers()[impl].DataDir(),
			"-network-magic 42",
			"-out /parquet/a",
			"sleep 60",
		} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s: command %q lacks %q", impl, cmd, want)
			}
		}
		if sc.Image != "mithril2pq:test" {
			t.Errorf("%s: image %s", impl, sc.Image)
		}
		if cpu := sc.Resources.Requests.Cpu().MilliValue(); cpu > 100 {
			t.Errorf("%s: sidecar requests %dm CPU", impl, cpu)
		}
		if mem := sc.Resources.Requests.Memory().Value(); mem > 256<<20 {
			t.Errorf("%s: sidecar requests %d bytes of memory", impl, mem)
		}
	}
}

// TestSidecarNeedsDataDir: a driver with no ImmutableDB has nothing for the
// sidecar to read, so rendering it is an error.
func TestSidecarNeedsDataDir(t *testing.T) {
	_, err := Render(stubDriver{impl: v1alpha1.CardanoNode}, Input{
		Network: withParquet(),
		Pool:    pool("ns", "a", v1alpha1.CardanoNode, v1alpha1.TierValidating),
		Image:   Image{Name: "r@sha256:aaaa"},
	})
	if err == nil {
		t.Fatal("rendered a sidecar for a driver with no DataDir")
	}
}

// TestSidecarOnlyOnPodsThatRender: a logical pool renders nothing, so it
// gets no sidecar either.
func TestSidecarOnlyOnPodsThatRender(t *testing.T) {
	w := renderOn(t, Drivers()[v1alpha1.Dingo], withParquet(), pool("ns", "l", v1alpha1.Dingo, v1alpha1.TierLogical))
	if w.StatefulSet != nil {
		t.Errorf("logical pool rendered a StatefulSet with a sidecar")
	}
}
