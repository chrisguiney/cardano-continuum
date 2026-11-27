package kube

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

// Client is the part of the Kubernetes API the commands use: the
// ContinuumNetwork, its ContinuumPools, and an image patch.
type Client interface {
	Network(ctx context.Context, name string) (*v1alpha1.ContinuumNetwork, error)
	Pools(ctx context.Context, network string) ([]v1alpha1.ContinuumPool, error)
	Apply(ctx context.Context, c imageset.Change) error
}

// Opener connects to the cluster named by kubeconfig (empty: the usual
// KUBECONFIG rules) and works in namespace.
type Opener func(kubeconfig, namespace string) (Client, error)

// Cluster is Client over a controller-runtime client.
type Cluster struct {
	c  client.Client
	ns string
}

// Open connects to the cluster named by kubeconfig; it is an Opener.
func Open(kubeconfig, namespace string) (Client, error) {
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
	return &Cluster{c: c, ns: namespace}, nil
}

// Network returns the named ContinuumNetwork.
func (k *Cluster) Network(ctx context.Context, name string) (*v1alpha1.ContinuumNetwork, error) {
	var n v1alpha1.ContinuumNetwork
	if err := k.c.Get(ctx, types.NamespacedName{Namespace: k.ns, Name: name}, &n); err != nil {
		return nil, fmt.Errorf("get network %s/%s: %w", k.ns, name, err)
	}
	return &n, nil
}

// Pools lists the ContinuumPools of network.
func (k *Cluster) Pools(ctx context.Context, network string) ([]v1alpha1.ContinuumPool, error) {
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
func (k *Cluster) Apply(ctx context.Context, c imageset.Change) error {
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

// PatchPool merge-patches the listed spec fields of ContinuumPool name.
func (k *Cluster) PatchPool(ctx context.Context, name string, spec map[string]any) error {
	patch, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return fmt.Errorf("encode patch: %w", err)
	}
	obj := &v1alpha1.ContinuumPool{}
	obj.SetNamespace(k.ns)
	obj.SetName(name)
	if err := k.c.Patch(ctx, obj, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("patch pool %s/%s: %w", k.ns, name, err)
	}
	return nil
}
