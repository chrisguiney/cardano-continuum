package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/imageset"
)

// kubeCluster is imageCluster over a controller-runtime client.
type kubeCluster struct {
	c  client.Client
	ns string
}

func openKube(kubeconfig, namespace string) (imageCluster, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfig
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	s := runtime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(s), v1alpha1.AddToScheme(s)); err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		return nil, fmt.Errorf("connect to cluster: %w", err)
	}
	return &kubeCluster{c: c, ns: namespace}, nil
}

func (k *kubeCluster) Network(ctx context.Context, name string) (*v1alpha1.ContinuumNetwork, error) {
	var n v1alpha1.ContinuumNetwork
	if err := k.c.Get(ctx, types.NamespacedName{Namespace: k.ns, Name: name}, &n); err != nil {
		return nil, fmt.Errorf("get network %s/%s: %w", k.ns, name, err)
	}
	return &n, nil
}

func (k *kubeCluster) Pools(ctx context.Context, network string) ([]v1alpha1.ContinuumPool, error) {
	var l v1alpha1.ContinuumPoolList
	if err := k.c.List(ctx, &l, client.InNamespace(k.ns)); err != nil {
		return nil, fmt.Errorf("list pools in %s: %w", k.ns, err)
	}
	var out []v1alpha1.ContinuumPool
	for _, p := range l.Items {
		if p.Spec.NetworkRef == network {
			out = append(out, p)
		}
	}
	return out, nil
}

// Apply merge-patches only the one field, so a concurrent edit of anything
// else on the object is not overwritten.
func (k *kubeCluster) Apply(ctx context.Context, c imageset.Change) error {
	var obj client.Object
	var body map[string]any
	switch c.Kind {
	case imageset.KindNetwork:
		obj = &v1alpha1.ContinuumNetwork{}
		body = map[string]any{"spec": map[string]any{"images": map[string]string{string(c.Impl): c.To}}}
	default:
		obj = &v1alpha1.ContinuumPool{}
		body = map[string]any{"spec": map[string]any{"image": c.To}}
	}
	obj.SetNamespace(k.ns)
	obj.SetName(c.Name)
	patch, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode patch: %w", err)
	}
	return k.c.Patch(ctx, obj, client.RawPatch(types.MergePatchType, patch))
}
