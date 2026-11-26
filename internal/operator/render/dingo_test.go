package render

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
)

// parseDingo parses the dingo config file of a rendered workload.
func parseDingo(t *testing.T, w Workload) map[string]any {
	t.Helper()
	raw, ok := w.Config.Data[DingoConfigFile]
	if !ok {
		t.Fatalf("no %s in the config ConfigMap: %v", DingoConfigFile, w.Config.Data)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("parse %s: %v", DingoConfigFile, err)
	}
	return cfg
}

// dingoArgs is the container's command line.
func dingoArgs(spec corev1.PodSpec) string {
	c := spec.Containers[0]
	return strings.Join(append(slices.Clone(c.Command), c.Args...), " ")
}

func TestRenderDingoRelay(t *testing.T) {
	w := render(t, Dingo{}, pool("ns", "r", v1alpha1.Dingo, v1alpha1.TierValidating), "r@sha256:aaaa")
	spec := w.StatefulSet.Spec.Template.Spec
	if slices.ContainsFunc(spec.Volumes, func(v corev1.Volume) bool { return v.Secret != nil }) {
		t.Error("relay mounts a Secret")
	}
	if len(spec.InitContainers) != 0 {
		t.Error("relay has a keys gate")
	}
	cfg := parseDingo(t, w)
	if got, want := cfg["cardanoConfig"], GenesisDir+"/"+genesis.ConfigFile; got != want {
		t.Errorf("cardanoConfig %v, want the genesis ConfigMap's %s", got, want)
	}
	if got := cfg["networkMagic"]; got != float64(42) {
		t.Errorf("networkMagic %v, want 42", got)
	}
	if got := cfg["blockProducer"]; got != false {
		t.Errorf("blockProducer %v, want false", got)
	}
	for _, k := range []string{"shelleyVrfKey", "shelleyKesKey", "shelleyOperationalCertificate", "shelleyKesAgentSocket"} {
		if _, ok := cfg[k]; ok {
			t.Errorf("relay config sets %s", k)
		}
	}
	if !strings.Contains(w.Config.Data[dingoTopology], "pool-b.ns.svc.cluster.local") {
		t.Errorf("topology does not list the peer: %s", w.Config.Data[dingoTopology])
	}
	if got, want := cfg["topology"], ConfigDir+"/"+dingoTopology; got != want {
		t.Errorf("topology %v, want %s", got, want)
	}
	if !strings.Contains(dingoArgs(spec), "--config "+ConfigDir+"/"+DingoConfigFile) {
		t.Errorf("command %q does not load the rendered config", dingoArgs(spec))
	}
	golden(t, "dingo-relay", w)
}

// TestRenderDingoForging: the config points dingo's one key set at the
// Secret mount, and dingo can read the keys there. dingo's validate.go
// refuses shelleyKesKey together with shelleyKesAgentSocket, and its
// keystore refuses a key file with group or other access, so the files stay
// at 0400 and the node runs as their owner.
func TestRenderDingoForging(t *testing.T) {
	w := render(t, Dingo{}, pool("ns", "a", v1alpha1.Dingo, v1alpha1.TierForging), "r@sha256:aaaa")
	spec := w.StatefulSet.Spec.Template.Spec
	cfg := parseDingo(t, w)
	if got := cfg["blockProducer"]; got != true {
		t.Errorf("blockProducer %v, want true", got)
	}
	for k, want := range map[string]string{
		"shelleyVrfKey":                 KeysDir + "/" + VRFKey,
		"shelleyKesKey":                 KeysDir + "/" + KESKey,
		"shelleyOperationalCertificate": KeysDir + "/" + OpCert,
	} {
		if got := cfg[k]; got != want {
			t.Errorf("%s %v, want %s", k, got, want)
		}
	}
	if _, ok := cfg["shelleyKesAgentSocket"]; ok {
		t.Error("config sets shelleyKesAgentSocket next to shelleyKesKey")
	}

	i := slices.IndexFunc(spec.Volumes, func(v corev1.Volume) bool { return v.Secret != nil })
	if i < 0 {
		t.Fatal("no Secret volume")
	}
	if sv := spec.Volumes[i].Secret; sv.SecretName != "a-keys" || sv.DefaultMode == nil || *sv.DefaultMode != 0o400 {
		t.Errorf("Secret volume %s mode %v, want a-keys at 0400", sv.SecretName, sv.DefaultMode)
	}
	node := spec.Containers[0]
	if !slices.ContainsFunc(node.VolumeMounts, func(m corev1.VolumeMount) bool {
		return m.Name == spec.Volumes[i].Name && m.MountPath == KeysDir && m.ReadOnly
	}) {
		t.Errorf("keys not mounted read-only at %s: %+v", KeysDir, node.VolumeMounts)
	}
	if sc := node.SecurityContext; sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 0 {
		t.Errorf("security context %+v: the node must run as the owner of the 0400 key files", sc)
	}
	if spec.SecurityContext != nil && spec.SecurityContext.FSGroup != nil {
		t.Error("an fsGroup makes the key files group-readable, which dingo refuses")
	}
	if len(spec.InitContainers) != 1 {
		t.Errorf("forger has %d init containers, want the keys gate", len(spec.InitContainers))
	}
	golden(t, "dingo-forging", w)
}

// TestDingoEnvMatchesConfig: the image bakes in environment variables that
// dingo applies over its config file. The rendered container sets each to
// the config's own value, so the file is what the node runs with.
func TestDingoEnvMatchesConfig(t *testing.T) {
	w := render(t, Dingo{}, pool("ns", "a", v1alpha1.Dingo, v1alpha1.TierForging), "r@sha256:aaaa")
	cfg := parseDingo(t, w)
	env := map[string]string{}
	for _, e := range w.StatefulSet.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	for name, key := range map[string]string{
		"CARDANO_NETWORK":       "network",
		"CARDANO_DATABASE_PATH": "databasePath",
		"CARDANO_SOCKET_PATH":   "socketPath",
	} {
		got, ok := env[name]
		if !ok {
			t.Errorf("%s not set: the image's value would override %s", name, key)
			continue
		}
		if got != cfg[key] {
			t.Errorf("%s=%q, config %s=%v", name, got, key, cfg[key])
		}
	}
}

func TestDingoDataDir(t *testing.T) {
	if got, want := (Dingo{}).DataDir(), DataMount+"/db/immutable"; got != want {
		t.Fatalf("DataDir %q, want %q", got, want)
	}
	if got := (Dingo{}).Image(); got != "ghcr.io/blinklabs-io/dingo:main" {
		t.Fatalf("Image %q, want dingo's main tag (decision D13)", got)
	}
}
