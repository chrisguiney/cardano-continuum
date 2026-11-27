package render

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		Sidecar: n.Spec.Parquet.Enabled,
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestSidecarCommand: mithril2pq -watch gets the driver's ImmutableDB, the network
// magic and a per-pool output directory to mithril2pq.
func TestSidecarCommand(t *testing.T) {
	t.Parallel()
	for _, impl := range []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.Dingo} {
		p := pool("ns", "a", impl, v1alpha1.TierValidating)
		w := renderOn(t, Drivers()[impl], withParquet(), p)
		sc, _ := sidecarOf(w.StatefulSet.Spec.Template.Spec)
		cmd := strings.Join(append(sc.Command, sc.Args...), " ")
		for _, want := range []string{
			"/usr/local/bin/mithril2pq -watch 60s -immutable-dir " + Drivers()[impl].DataDir(),
			"-network-magic 42",
			"-out /parquet/a",
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
	t.Parallel()
	_, err := Render(stubDriver{impl: v1alpha1.CardanoNode}, Input{
		Network: withParquet(),
		Pool:    pool("ns", "a", v1alpha1.CardanoNode, v1alpha1.TierValidating),
		Image:   Image{Name: "r@sha256:aaaa"},
		Sidecar: true,
	})
	if err == nil {
		t.Fatal("rendered a sidecar for a driver with no DataDir")
	}
}

// TestSidecarOnlyOnPodsThatRender: a logical pool renders nothing, so it
// gets no sidecar either.
func TestSidecarOnlyOnPodsThatRender(t *testing.T) {
	t.Parallel()
	w := renderOn(t, Drivers()[v1alpha1.Dingo], withParquet(), pool("ns", "l", v1alpha1.Dingo, v1alpha1.TierLogical))
	if w.StatefulSet != nil {
		t.Errorf("logical pool rendered a StatefulSet with a sidecar")
	}
}

// TestSidecarRunsNoShell: the mithril2pq image is distroless, so the
// container must exec the binary directly with its own flags.
func TestSidecarRunsNoShell(t *testing.T) {
	t.Parallel()
	for _, impl := range []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.Dingo} {
		w := renderOn(t, Drivers()[impl], withParquet(), pool("ns", "a", impl, v1alpha1.TierValidating))
		sc, _ := sidecarOf(w.StatefulSet.Spec.Template.Spec)
		if len(sc.Command) == 0 || sc.Command[0] != "/usr/local/bin/mithril2pq" {
			t.Errorf("%s: command %q, want the mithril2pq binary first", impl, sc.Command)
		}
		for _, a := range append(sc.Command, sc.Args...) {
			if a == "sh" || a == "-c" || a == "/bin/sh" || strings.ContainsAny(a, ";&|$`") {
				t.Errorf("%s: shell syntax %q in the sidecar command", impl, a)
			}
		}
	}
}

// TestPoolDemandCountsOneSidecar: the sidecar's requests count against the
// budget once, for the source pool only.
func TestPoolDemandCountsOneSidecar(t *testing.T) {
	t.Parallel()
	d := Drivers()[v1alpha1.Dingo]
	if got := PoolDemand(d, false); got != d.Requests() {
		t.Errorf("non-source: %+v, want %+v", got, d.Requests())
	}
	got := PoolDemand(d, true)
	want := Demand{CPUMillis: d.Requests().CPUMillis + sidecarCPU, MemoryMiB: d.Requests().MemoryMiB + sidecarMemMiB}
	if got != want {
		t.Fatalf("source: %+v, want %+v", got, want)
	}
	b := v1alpha1.BudgetSpec{MaxPools: 2, CPUMillis: 2 * d.Requests().CPUMillis, MemoryMiB: 2 * d.Requests().MemoryMiB}
	if err := Admit(b, []Claim{{"a", PoolDemand(d, false)}, {"b", PoolDemand(d, false)}}, "b"); err != nil {
		t.Fatalf("nodes alone refused: %v", err)
	}
	if err := Admit(b, []Claim{{"a", PoolDemand(d, true)}, {"b", PoolDemand(d, false)}}, "b"); err == nil {
		t.Error("budget sized for the nodes admitted both pools beside a sidecar")
	}
}

func poolWithID(name, id string, tier v1alpha1.Tier) v1alpha1.ContinuumPool {
	p := pool("ns", name, v1alpha1.Dingo, tier)
	p.Spec.PoolID = id
	return *p
}

// TestParquetSource: the default is the lowest-named relay, else the
// lowest-named keyed pool, whatever order the pools arrive in; spec.Pool
// overrides; off or no candidate is "".
func TestParquetSource(t *testing.T) {
	t.Parallel()
	on := v1alpha1.ParquetSpec{Enabled: true}
	f := poolWithID("f", "pf", v1alpha1.TierForging)
	g := poolWithID("g", "pg", v1alpha1.TierForging)
	r1 := poolWithID("r1", "p1", v1alpha1.TierValidating)
	r2 := poolWithID("r2", "p2", v1alpha1.TierValidating)
	logical := poolWithID("a", "pa", v1alpha1.TierLogical)
	other := poolWithID("0", "p0", v1alpha1.TierValidating)
	other.Spec.NetworkRef = "elsewhere"
	gone := poolWithID("b", "pb", v1alpha1.TierValidating)
	gone.DeletionTimestamp = &metav1.Time{}

	tests := []struct {
		name  string
		spec  v1alpha1.ParquetSpec
		pools []v1alpha1.ContinuumPool
		want  string
	}{
		{"relay beats forger", on, []v1alpha1.ContinuumPool{f, g, r2, r1}, "r1"},
		{"forgers only: lowest name", on, []v1alpha1.ContinuumPool{g, f}, "f"},
		{"skips logical, other networks, deleting", on, []v1alpha1.ContinuumPool{logical, other, gone, r2}, "r2"},
		{"override by pool id", v1alpha1.ParquetSpec{Enabled: true, Pool: "pg"}, []v1alpha1.ContinuumPool{f, g, r1}, "g"},
		{"override names no pool", v1alpha1.ParquetSpec{Enabled: true, Pool: "nope"}, []v1alpha1.ContinuumPool{f, r1}, ""},
		{"off", v1alpha1.ParquetSpec{Pool: "pg"}, []v1alpha1.ContinuumPool{f, g}, ""},
		{"no pools", on, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for range 3 {
				if got := ParquetSource("net", tt.spec, tt.pools); got != tt.want {
					t.Fatalf("source %q, want %q", got, tt.want)
				}
				slices.Reverse(tt.pools)
			}
		})
	}
}

// TestOnlySourcePodGetsSidecar: across the network's pools, exactly the
// source renders a sidecar, and every other pod renders as it does with the
// sidecar off.
func TestOnlySourcePodGetsSidecar(t *testing.T) {
	t.Parallel()
	n := withParquet()
	pools := []v1alpha1.ContinuumPool{
		poolWithID("f", "pf", v1alpha1.TierForging),
		poolWithID("r1", "p1", v1alpha1.TierValidating),
		poolWithID("r2", "p2", v1alpha1.TierValidating),
	}
	src := ParquetSource("net", n.Spec.Parquet, pools)
	if src != "r1" {
		t.Fatalf("source %q, want r1", src)
	}
	in := func(net *v1alpha1.ContinuumNetwork, p *v1alpha1.ContinuumPool, side bool) Workload {
		w, err := Render(Drivers()[v1alpha1.Dingo], Input{Network: net, Pool: p,
			Image: Image{Name: "r@sha256:aaaa"}, Sidecar: side})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	with := 0
	for i := range pools {
		p := &pools[i]
		got := in(n, p, p.Name == src)
		_, has := sidecarOf(got.StatefulSet.Spec.Template.Spec)
		if has != (p.Name == src) {
			t.Errorf("pool %s: sidecar %v", p.Name, has)
		}
		if has {
			with++
			continue
		}
		a, _ := yaml.Marshal(got)
		b, _ := yaml.Marshal(in(network("ns"), p, false))
		if string(a) != string(b) {
			t.Errorf("pool %s: changed by a sidecar elsewhere", p.Name)
		}
	}
	if with != 1 {
		t.Errorf("%d pods with a sidecar, want 1", with)
	}
}
