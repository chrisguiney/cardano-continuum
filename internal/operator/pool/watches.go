package pool

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/fence"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

func poolOfObject(_ context.Context, o client.Object) []reconcile.Request {
	name := o.GetAnnotations()[render.AnnotationPoolName]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: name}}}
}

// poolsWhere maps an event to every pool in ns that keep selects.
func poolsWhere(ctx context.Context, c client.Reader, ns string, keep func(*v1alpha1.ContinuumPool) bool) []reconcile.Request {
	var list v1alpha1.ContinuumPoolList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil
	}
	var out []reconcile.Request
	for i := range list.Items {
		if keep(&list.Items[i]) {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return out
}

// siblingsOf re-admits a network's pools when one changes: the budget and
// the topology depend on all of them.
func siblingsOf(c client.Reader) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []reconcile.Request {
		net := o.(*v1alpha1.ContinuumPool).Spec.NetworkRef
		return poolsWhere(ctx, c, o.GetNamespace(), func(p *v1alpha1.ContinuumPool) bool {
			return p.Spec.NetworkRef == net && p.Name != o.GetName()
		})
	}
}

func poolsOfNetwork(c client.Reader) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []reconcile.Request {
		return poolsWhere(ctx, c, o.GetNamespace(), func(p *v1alpha1.ContinuumPool) bool { return p.Spec.NetworkRef == o.GetName() })
	}
}

func poolsOfSecret(c client.Reader) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []reconcile.Request {
		return poolsWhere(ctx, c, o.GetNamespace(), func(p *v1alpha1.ContinuumPool) bool { return p.Spec.KeysSecret == o.GetName() })
	}
}

func poolsOnNode(c client.Reader) handler.MapFunc {
	pods := fence.PodsOnNode(c)
	return func(ctx context.Context, o client.Object) []reconcile.Request {
		var out []reconcile.Request
		for _, req := range pods(ctx, o) {
			var pod corev1.Pod
			if c.Get(ctx, req.NamespacedName, &pod) == nil {
				out = append(out, poolOfObject(ctx, &pod)...)
			}
		}
		return out
	}
}
