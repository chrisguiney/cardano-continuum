package render

import (
	"fmt"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/check"
)

// Input is everything Render needs for one pool.
type Input struct {
	Network *v1alpha1.ContinuumNetwork
	Pool    *v1alpha1.ContinuumPool
	Peers   []Peer
	Image   Image
	// Sidecar adds the mithril2pq sidecar, when the network enables it. Only
	// the network's source pool (ParquetSource) sets it.
	Sidecar bool
}

// Image is the container image a node runs, pinned to a digest when known.
type Image struct {
	Name       string
	PullPolicy corev1.PullPolicy
}

// Workload is what one pool renders. Every field is nil for a pool that
// renders nothing.
type Workload struct {
	StatefulSet *appsv1.StatefulSet `json:"statefulSet,omitempty"`
	Service     *corev1.Service     `json:"service,omitempty"`
	Config      *corev1.ConfigMap   `json:"config,omitempty"`
	// Reload is Node.Reload: what makes the running node reread Config.
	Reload []string `json:"reload,omitempty"`
}

// NodeContainer is the name of the container that runs the node.
const NodeContainer = "node"

// Volume names inside the pod.
const (
	volGenesis = "genesis"
	volConfig  = "config"
	volKeys    = "keys"
	volData    = "data"
)

// Render builds the workload of in.Pool with driver d. It is pure.
//
// Everything the fence depends on is decided here and not by the driver, so
// every driver gets it: the name (Name), the finalizer on a keyed template,
// the keys Secret mount and the keys gate. A keyed StatefulSet has one
// replica, or none when the pool is dark.
func Render(d NodeDriver, in Input) (Workload, error) {
	p := in.Pool
	role := RoleOf(p)
	if role == RoleNone {
		return Workload{}, nil
	}
	if role.Keyed() && p.Spec.KeysSecret == "" {
		return Workload{}, fmt.Errorf("pool %s: forging tier needs keysSecret", p.Name)
	}
	n, err := d.Node(NodeSpec{Magic: in.Network.Spec.Magic, Peers: in.Peers, Keyed: role.Keyed()})
	if err != nil {
		return Workload{}, fmt.Errorf("render %s node for pool %s: %w", d.Impl(), p.Name, err)
	}
	if role.Keyed() && len(n.Gate) == 0 {
		return Workload{}, fmt.Errorf("render %s node for pool %s: driver has no keys gate", d.Impl(), p.Name)
	}
	name := Name(p)
	meta := func() metav1.ObjectMeta { return objectMeta(p, name, d.Impl()) }
	w := Workload{
		StatefulSet: statefulSet(meta(), podTemplate(d, in, n, meta()), p.Spec.Dark),
		Service:     service(meta(), len(n.NtC) > 0),
		Config:      &corev1.ConfigMap{ObjectMeta: withName(meta(), ConfigName(name)), Data: n.Config},
		Reload:      n.Reload,
	}
	if pq := in.Network.Spec.Parquet; pq.Enabled && in.Sidecar {
		w, err = WithSidecar(w, d, Sidecar{
			Image: pq.Image, Pool: p.Name, Magic: in.Network.Spec.Magic,
			IntervalSeconds: pq.IntervalSeconds, OutputClaim: pq.OutputClaim,
		})
		if err != nil {
			return Workload{}, fmt.Errorf("render sidecar for pool %s: %w", p.Name, err)
		}
	}
	check.Post(w.StatefulSet.Name == name, "operator.Render: StatefulSet name is Name(pool)")
	check.Post(!role.Keyed() || slices.Contains(w.StatefulSet.Spec.Template.Finalizers, Finalizer),
		"operator.Render: keyed template carries the finalizer")
	return w, nil
}

func ConfigName(workload string) string { return workload + "-config" }

func labelsOf(p *v1alpha1.ContinuumPool, name string, impl v1alpha1.Implementation) map[string]string {
	return map[string]string{
		LabelPool:     PoolKey(p.Spec.PoolID),
		LabelRole:     string(RoleOf(p)),
		LabelImpl:     string(impl),
		LabelWorkload: name,
		LabelOwner:    string(p.UID),
	}
}

func objectMeta(p *v1alpha1.ContinuumPool, name string, impl v1alpha1.Implementation) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:        name,
		Namespace:   p.Namespace,
		Labels:      labelsOf(p, name, impl),
		Annotations: map[string]string{AnnotationPoolName: p.Name},
		OwnerReferences: []metav1.OwnerReference{
			*metav1.NewControllerRef(p, v1alpha1.GroupVersion.WithKind("ContinuumPool")),
		},
	}
}

func withName(m metav1.ObjectMeta, name string) metav1.ObjectMeta {
	m.Name = name
	return m
}

func statefulSet(meta metav1.ObjectMeta, tmpl corev1.PodTemplateSpec, dark bool) *appsv1.StatefulSet {
	replicas := int32(1)
	if dark {
		replicas = 0
	}
	return &appsv1.StatefulSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"},
		ObjectMeta: meta,
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{LabelWorkload: meta.Name}},
			ServiceName: meta.Name,
			Template:    tmpl,
			// OrderedReady with one replica: the controller deletes the old
			// pod and creates its replacement only once the old object is
			// gone, finalizer included (contract rule 9). No surge exists
			// for a StatefulSet.
			PodManagementPolicy: appsv1.OrderedReadyPodManagement,
			UpdateStrategy:      appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType},
		},
	}
}

func service(meta metav1.ObjectMeta, ntc bool) *corev1.Service {
	s := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: meta,
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  map[string]string{LabelWorkload: meta.Name},
			// Peers dial a syncing node before it is ready.
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{{
				Name: "n2n", Port: N2NPort, TargetPort: intstr.FromInt32(N2NPort),
			}},
		},
	}
	if ntc {
		s.Spec.Ports = append(s.Spec.Ports, corev1.ServicePort{
			Name: "ntc", Port: NtCPort, TargetPort: intstr.FromInt32(NtCPort),
		})
	}
	return s
}

func podTemplate(d NodeDriver, in Input, n Node, meta metav1.ObjectMeta) corev1.PodTemplateSpec {
	keyed := RoleOf(in.Pool).Keyed()
	c := n.Container
	c.Name = NodeContainer
	c.Image = in.Image.Name
	c.ImagePullPolicy = in.Image.PullPolicy
	c.Resources = requests(d.Requests())
	c.Ports = []corev1.ContainerPort{{Name: "n2n", ContainerPort: N2NPort}}
	c.VolumeMounts = append(c.VolumeMounts,
		corev1.VolumeMount{Name: volGenesis, MountPath: GenesisDir, ReadOnly: true},
		corev1.VolumeMount{Name: volConfig, MountPath: ConfigDir, ReadOnly: true},
		corev1.VolumeMount{Name: volData, MountPath: DataMount},
	)
	containers := []corev1.Container{c}
	if len(n.NtC) > 0 {
		containers = append(containers, corev1.Container{
			Name: "ntc", Image: c.Image, ImagePullPolicy: c.ImagePullPolicy, Command: n.NtC,
			Ports:        []corev1.ContainerPort{{Name: "ntc", ContainerPort: NtCPort}},
			Resources:    requests(Demand{CPUMillis: 10, MemoryMiB: 16}),
			VolumeMounts: []corev1.VolumeMount{{Name: volData, MountPath: DataMount}},
		})
	}
	spec := corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		Containers:                   containers,
		Volumes:                      volumes(in, meta.Name),
	}
	for _, s := range in.Network.Spec.ImagePullSecrets {
		spec.ImagePullSecrets = append(spec.ImagePullSecrets, corev1.LocalObjectReference{Name: s})
	}
	tmpl := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: meta.Labels, Annotations: meta.Annotations},
		Spec:       spec,
	}
	if keyed {
		keys := corev1.VolumeMount{Name: volKeys, MountPath: KeysDir, ReadOnly: true}
		tmpl.Spec.Containers[0].VolumeMounts = append(tmpl.Spec.Containers[0].VolumeMounts, keys)
		tmpl.Spec.InitContainers = []corev1.Container{{
			Name: "keys-gate", Image: c.Image, ImagePullPolicy: c.ImagePullPolicy,
			Command: n.Gate, VolumeMounts: []corev1.VolumeMount{keys},
		}}
		// On the template, so on the pod from its first moment: there is
		// no window in which a keyed pod exists without it (rule 3).
		tmpl.Finalizers = []string{Finalizer}
	}
	return tmpl
}

func volumes(in Input, name string) []corev1.Volume {
	vols := []corev1.Volume{
		{Name: volGenesis, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: in.Network.Spec.GenesisRef},
		}}},
		{Name: volConfig, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: ConfigName(name)},
		}}},
		{Name: volData, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if !RoleOf(in.Pool).Keyed() {
		return vols
	}
	// Required (not Optional): a pod whose Secret is gone never starts.
	// That is what keeps a scenario duplicate from outliving its Secret.
	return append(vols, corev1.Volume{Name: volKeys, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName:  in.Pool.Spec.KeysSecret,
		DefaultMode: ptr.To(int32(0o400)),
		Optional:    ptr.To(false),
	}}})
}

func requests(d Demand) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(d.CPUMillis, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(d.MemoryMiB<<20, resource.BinarySI),
	}}
}
