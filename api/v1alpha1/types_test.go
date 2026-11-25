package v1alpha1

import (
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/chrisguiney/cardano-continuum/internal/ident"
)

var fixedTime = metav1.NewTime(time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC))

func fullNetwork() *ContinuumNetwork {
	return &ContinuumNetwork{
		TypeMeta:   metav1.TypeMeta{APIVersion: ident.APIGroup + "/v1alpha1", Kind: "ContinuumNetwork"},
		ObjectMeta: metav1.ObjectMeta{Name: "devnet", Namespace: "continuum"},
		Spec: ContinuumNetworkSpec{
			Magic:            42,
			GenesisRef:       "genesis-42",
			Topology:         TopologyPolicy{Policy: TopologyRelayed},
			Budget:           BudgetSpec{MaxPools: 10, CPUMillis: 8000, MemoryMiB: 16384},
			Images:           ImageSet{Dingo: "ghcr.io/example/dingo:main"},
			ImagePullSecrets: []string{"regcred"},
			EpochLength:      500,
			SecurityParam:    10,
		},
		Status: ContinuumNetworkStatus{
			Phase:             "Running",
			GenesisHash:       "abc123",
			ForgingStakeRatio: "0.75",
			Images: map[Implementation]ResolvedImage{
				Dingo: fullResolvedImage(),
			},
			Tips: map[string]TipStatus{
				"pool-a": {Slot: 1234, Hash: "deadbeef"},
			},
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				Reason:             "Synced",
				Message:            "all pools synced",
				LastTransitionTime: fixedTime,
			}},
		},
	}
}

func fullResolvedImage() ResolvedImage {
	return ResolvedImage{
		Ref:    "ghcr.io/example/dingo:main",
		Digest: "sha256:0123",
		Commit: "9911e55",
		Build: BuildInfo{
			Mode:      BuildModeRepro,
			Dirty:     true,
			Revisions: map[string]string{"gouroboros": "v0.220.4", "plutigo": "v0.12.2"},
		},
	}
}

func fullPool() *ContinuumPool {
	return &ContinuumPool{
		TypeMeta:   metav1.TypeMeta{APIVersion: ident.APIGroup + "/v1alpha1", Kind: "ContinuumPool"},
		ObjectMeta: metav1.ObjectMeta{Name: "pool-a", Namespace: "continuum"},
		Spec: ContinuumPoolSpec{
			NetworkRef:        "devnet",
			PoolID:            "pool1xyz",
			Implementation:    Dingo,
			Tier:              TierForging,
			KeysSecret:        "pool-a-keys",
			Image:             "ghcr.io/example/dingo:dev-9911e55",
			Dark:              true,
			Analytics:         true,
			UnsafeDuplicateOf: "pool-b",
		},
		Status: ContinuumPoolStatus{
			Phase:    PhaseForging,
			Tip:      TipStatus{Slot: 1234, Hash: "deadbeef"},
			Workload: "pool-a-dingo",
			Image:    fullResolvedImage(),
			Build:    BuildInfo{Mode: BuildModeFast, Dirty: false},
			Conditions: []metav1.Condition{{
				Type:               "Admitted",
				Status:             metav1.ConditionFalse,
				Reason:             "Fenced",
				Message:            "pool1xyz is held by pool-b",
				LastTransitionTime: fixedTime,
			}},
		},
	}
}

func roundTrip[T any](t *testing.T, in *T) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Compare encodings, not structs: metav1.Time decodes into local time, so
	// the decoded value differs in location from the input while encoding the same.
	raw2, err := json.Marshal(&out)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if string(raw) != string(raw2) {
		t.Errorf("round trip changed the encoding\nfirst:  %s\nsecond: %s", raw, raw2)
	}
}

func TestRoundTripContinuumNetwork(t *testing.T) {
	roundTrip(t, fullNetwork())
}

func TestRoundTripContinuumPool(t *testing.T) {
	roundTrip(t, fullPool())
}

func TestRoundTripLists(t *testing.T) {
	roundTrip(t, &ContinuumNetworkList{Items: []ContinuumNetwork{*fullNetwork()}})
	roundTrip(t, &ContinuumPoolList{Items: []ContinuumPool{*fullPool()}})
}

func TestGroupVersionMatchesIdent(t *testing.T) {
	if GroupVersion.Group != ident.APIGroup {
		t.Errorf("Group = %q, want ident.APIGroup %q", GroupVersion.Group, ident.APIGroup)
	}
	if GroupVersion.Version != "v1alpha1" {
		t.Errorf("Version = %q, want v1alpha1", GroupVersion.Version)
	}
}

func TestAddToSchemeRegistersKinds(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []runtime.Object{&ContinuumNetwork{}, &ContinuumPool{}} {
		gvks, _, err := s.ObjectKinds(obj)
		if err != nil {
			t.Fatalf("%T not registered: %v", obj, err)
		}
		if gvks[0].Group != ident.APIGroup || gvks[0].Version != "v1alpha1" {
			t.Errorf("%T registered as %v", obj, gvks[0])
		}
	}
}

func TestDeepCopyIsIndependent(t *testing.T) {
	orig := fullNetwork()
	cp := orig.DeepCopy()
	cp.Spec.ImagePullSecrets[0] = "changed"
	cp.Status.Images[Dingo] = ResolvedImage{}
	cp.Status.Images[Dingo] = fullResolvedImage()
	cp.Status.Images[Dingo].Build.Revisions["gouroboros"] = "changed"
	cp.Status.Conditions[0].Reason = "changed"
	if orig.Spec.ImagePullSecrets[0] != "regcred" {
		t.Error("DeepCopy shares ImagePullSecrets backing array")
	}
	if orig.Status.Images[Dingo].Build.Revisions["gouroboros"] != "v0.220.4" {
		t.Error("DeepCopy shares the Revisions map")
	}
	if orig.Status.Conditions[0].Reason != "Synced" {
		t.Error("DeepCopy shares the Conditions slice")
	}

	pool := fullPool()
	pcp := pool.DeepCopy()
	pcp.Status.Image.Build.Revisions["plutigo"] = "changed"
	if pool.Status.Image.Build.Revisions["plutigo"] != "v0.12.2" {
		t.Error("ContinuumPool DeepCopy shares the image Revisions map")
	}
}
