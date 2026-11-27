package publish

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
)

var (
	// ConfigFiles are the files of the bundle's config directory that the
	// ConfigMap holds, each under its own name.
	ConfigFiles = []string{genesis.ConfigFile, genesis.ByronFile, genesis.ShelleyFile, genesis.AlonzoFile, genesis.ConwayFile}
	// KeyFiles are the files of a pool's directory that its Secret holds:
	// the ones the operator mounts into a forging pool.
	KeyFiles = []string{"kes.skey", "vrf.skey", "opcert.cert"}
)

// Names are the objects a genesis is published to.
type Names struct {
	// ConfigMap names the ConfigMap of configuration and genesis files.
	ConfigMap string
	// Keys is a fmt pattern for genesis pool i's Secret, such as "keys-%d".
	Keys string
}

// Objects is a genesis as the objects the operator reads.
type Objects struct {
	// Config holds ConfigFiles.
	Config *corev1.ConfigMap
	// Keys holds genesis pool i's KeyFiles at index i.
	Keys []*corev1.Secret
}

// Load reads the bundle under dir for pools genesis pools and builds its
// objects in namespace ns.
func Load(dir string, pools int, ns string, n Names) (Objects, error) {
	config, err := readFiles(filepath.Join(dir, "config"), ConfigFiles)
	if err != nil {
		return Objects{}, err
	}

	keys := make([]map[string][]byte, pools)
	for i := range pools {
		if keys[i], err = readFiles(filepath.Join(dir, "pools", strconv.Itoa(i)), KeyFiles); err != nil {
			return Objects{}, err
		}
	}

	return Build(ns, n, config, keys), nil
}

// Build returns the objects for config (ConfigFiles by name) and keys
// (genesis pool i's KeyFiles at index i) in namespace ns.
func Build(ns string, n Names, config map[string][]byte, keys []map[string][]byte) Objects {
	data := make(map[string]string, len(config))
	for name, raw := range config {
		data[name] = string(raw)
	}

	secrets := make([]*corev1.Secret, len(keys))
	for i, k := range keys {
		secrets[i] = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf(n.Keys, i), Namespace: ns},
			Data:       k,
		}
	}

	return Objects{
		Config: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: n.ConfigMap, Namespace: ns},
			Data:       data,
		},
		Keys: secrets,
	}
}

func readFiles(dir string, names []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		out[name] = raw
	}
	return out, nil
}

// Apply creates every object, or replaces the data of one that exists.
func Apply(ctx context.Context, c kubernetes.Interface, o Objects) error {
	if err := applyConfig(ctx, c, o.Config); err != nil {
		return err
	}

	for _, s := range o.Keys {
		if err := applySecret(ctx, c, s); err != nil {
			return err
		}
	}
	return nil
}

func applyConfig(ctx context.Context, c kubernetes.Interface, want *corev1.ConfigMap) error {
	api := c.CoreV1().ConfigMaps(want.Namespace)
	_, err := api.Create(ctx, want, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		return wrap("create configmap", want.ObjectMeta, err)
	}

	cur, err := api.Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil {
		return wrap("get configmap", want.ObjectMeta, err)
	}

	cur.Data, cur.BinaryData = want.Data, nil
	_, err = api.Update(ctx, cur, metav1.UpdateOptions{})
	return wrap("update configmap", want.ObjectMeta, err)
}

func applySecret(ctx context.Context, c kubernetes.Interface, want *corev1.Secret) error {
	api := c.CoreV1().Secrets(want.Namespace)
	_, err := api.Create(ctx, want, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		return wrap("create secret", want.ObjectMeta, err)
	}

	cur, err := api.Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil {
		return wrap("get secret", want.ObjectMeta, err)
	}

	cur.Data, cur.StringData = want.Data, nil
	_, err = api.Update(ctx, cur, metav1.UpdateOptions{})
	return wrap("update secret", want.ObjectMeta, err)
}

// wrap names the operation and object of a non-nil err.
func wrap(op string, m metav1.ObjectMeta, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %s/%s: %w", op, m.Namespace, m.Name, err)
}
