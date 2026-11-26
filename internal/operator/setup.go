package operator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/chrisguiney/cardano-continuum/internal/operator/fence"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
	"github.com/chrisguiney/cardano-continuum/internal/operator/network"
	"github.com/chrisguiney/cardano-continuum/internal/operator/phase"
	"github.com/chrisguiney/cardano-continuum/internal/operator/pool"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
	"github.com/chrisguiney/cardano-continuum/internal/poolwatch"
)

// +kubebuilder:rbac:groups=continuum.blinklabs.io,resources=continuumpools;continuumnetworks,verbs=get;list;watch
// +kubebuilder:rbac:groups=continuum.blinklabs.io,resources=continuumpools/status;continuumnetworks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create;get
// +kubebuilder:rbac:groups="",resources=nodes;secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Options configure Setup.
type Options struct {
	Drivers render.Registry
	Images  image.Resolver
	// Exec tells running nodes to reread their Config; nil disables it.
	Exec pool.Exec
	Log  *slog.Logger
	// Dial opens node-to-node connections to pool pods for a network magic,
	// to judge Forging and Following; nil dials real nodes.
	Dial func(magic uint32) poolwatch.Dialer
	// Poll is how often followed pools are refreshed; 0 is five seconds.
	Poll time.Duration
}

// CacheOptions limits the manager's caches of pods, StatefulSets, Services
// and ConfigMaps to the operator's own objects.
func CacheOptions() (cache.Options, error) {
	req, err := labels.NewRequirement(render.LabelWorkload, selection.Exists, nil)
	if err != nil {
		return cache.Options{}, fmt.Errorf("operator cache selector: %w", err)
	}
	own := cache.ByObject{Label: labels.NewSelector().Add(*req)}
	return cache.Options{ByObject: map[client.Object]cache.ByObject{
		&corev1.Pod{}: own, &appsv1.StatefulSet{}: own, &corev1.Service{}: own, &corev1.ConfigMap{}: own,
	}}, nil
}

// Setup registers the indexes and the three controllers with mgr.
func Setup(ctx context.Context, mgr manager.Manager, o Options) error {
	if o.Drivers == nil || o.Images == nil || o.Log == nil {
		return errors.New("operator setup: render.Drivers, Images and Log are required")
	}
	w := phase.NewWatch(phase.WatchOptions{Live: mgr.GetAPIReader(), Dial: o.Dial, Poll: o.Poll, Log: o.Log})
	return errors.Join(
		fence.Index(ctx, mgr),
		mgr.Add(w),
		pool.NewPools(mgr.GetClient(), o.Drivers, o.Images, o.Exec, o.Log).WithWatch(w).SetupWithManager(mgr),
		fence.NewRelease(mgr.GetClient(), mgr.GetAPIReader(), o.Log).SetupWithManager(mgr),
		network.NewNetworks(mgr.GetClient(), mgr.GetAPIReader(), o.Log).SetupWithManager(mgr),
	)
}
