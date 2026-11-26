package fence

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// nodeField indexes pods by the Node they are bound to.
const nodeField = "spec.nodeName"

// Index adds the field indexes the watches use.
func Index(ctx context.Context, mgr manager.Manager) error {
	err := mgr.GetFieldIndexer().IndexField(ctx, &corev1.Pod{}, nodeField, func(o client.Object) []string {
		if n := o.(*corev1.Pod).Spec.NodeName; n != "" {
			return []string{n}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("index pods by node: %w", err)
	}
	return nil
}

// PodsOnNode maps a Node event to every pod bound to that Node.
func PodsOnNode(c client.Reader) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []reconcile.Request {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.MatchingFields{nodeField: o.GetName()}); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(pods.Items))
		for i := range pods.Items {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&pods.Items[i])})
		}
		return out
	}
}

// ReadNode reports the state of the Node a pod is bound to, "" meaning
// never bound.
func ReadNode(ctx context.Context, c client.Reader, name string) (NodeState, error) {
	if name == "" {
		return NodeGone, nil
	}
	var n corev1.Node
	err := c.Get(ctx, types.NamespacedName{Name: name}, &n)
	if apierrors.IsNotFound(err) {
		return NodeGone, nil
	}
	if err != nil {
		return NodeUnreachable, fmt.Errorf("get node %s: %w", name, err)
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			return NodeReady, nil
		}
	}
	return NodeUnreachable, nil
}
