package publish

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

var names = Names{ConfigMap: "devnet-genesis", Keys: "keys-%d"}

// writeBundle lays out dir as genesis.WriteDir does, with each file holding
// its own relative path.
func writeBundle(t *testing.T, pools int) string {
	t.Helper()
	dir := t.TempDir()
	files := []string{}
	for _, f := range ConfigFiles {
		files = append(files, filepath.Join("config", f))
	}

	for i := range pools {
		for _, f := range append([]string{"topology.json", "cold.vkey"}, KeyFiles...) {
			files = append(files, filepath.Join("pools", strconv.Itoa(i), f))
		}
	}

	for _, f := range files {
		path := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestKeyFilesMatchOperator(t *testing.T) {
	want := []string{render.KESKey, render.VRFKey, render.OpCert}
	if !slices.Equal(KeyFiles, want) {
		t.Fatalf("KeyFiles = %v, want the files the operator mounts %v", KeyFiles, want)
	}
	cfg := []string{genesis.ConfigFile, genesis.ByronFile, genesis.ShelleyFile, genesis.AlonzoFile, genesis.ConwayFile}
	if !slices.Equal(ConfigFiles, cfg) {
		t.Fatalf("ConfigFiles = %v, want %v", ConfigFiles, cfg)
	}
}

func TestLoad(t *testing.T) {
	dir := writeBundle(t, 2)
	o, err := Load(dir, 2, "lab", names)
	if err != nil {
		t.Fatal(err)
	}
	if o.Config.Name != "devnet-genesis" || o.Config.Namespace != "lab" || len(o.Config.Data) != len(ConfigFiles) {
		t.Fatalf("config map = %s/%s with %d keys", o.Config.Namespace, o.Config.Name, len(o.Config.Data))
	}
	if got := o.Config.Data[genesis.ShelleyFile]; got != "config/"+genesis.ShelleyFile {
		t.Fatalf("shelley genesis = %q", got)
	}
	if len(o.Keys) != 2 {
		t.Fatalf("%d key secrets, want 2", len(o.Keys))
	}

	for i, s := range o.Keys {
		if s.Name != "keys-"+strconv.Itoa(i) || s.Namespace != "lab" || len(s.Data) != len(KeyFiles) {
			t.Fatalf("secret %d = %s/%s with %d keys", i, s.Namespace, s.Name, len(s.Data))
		}
		if _, ok := s.Data["cold.vkey"]; ok {
			t.Fatalf("secret %d carries cold.vkey, which the pools do not mount", i)
		}
	}

	if _, err := Load(dir, 3, "lab", names); err == nil {
		t.Fatal("loaded pool 2, which the bundle does not hold")
	}
}

func TestApplyCreatesThenUpdates(t *testing.T) {
	ctx := t.Context()
	cs := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "keys-1", Namespace: "lab"}, Data: map[string][]byte{"stale": nil}})
	o, err := Load(writeBundle(t, 2), 2, "lab", names)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, cs, o); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, cs, o); err != nil {
		t.Fatalf("a second apply: %v", err)
	}

	cm, err := cs.CoreV1().ConfigMaps("lab").Get(ctx, "devnet-genesis", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cm.Data) != len(ConfigFiles) {
		t.Fatalf("config map has %d keys", len(cm.Data))
	}

	for i := range 2 {
		s, err := cs.CoreV1().Secrets("lab").Get(ctx, "keys-"+strconv.Itoa(i), metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, stale := s.Data["stale"]; stale || len(s.Data) != len(KeyFiles) {
			t.Fatalf("secret %d data keys = %d (stale %v), want the published keys only", i, len(s.Data), stale)
		}
	}

	if _, err := cs.CoreV1().Secrets("other").Get(ctx, "keys-0", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("published outside the namespace: %v", err)
	}
}
