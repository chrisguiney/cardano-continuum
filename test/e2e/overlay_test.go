//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/devbuild"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/operator"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
)

const (
	// clusterRegistry is the registry's address on the kind network, which
	// the operator resolves against and the nodes pull from.
	clusterRegistry = "continuum-e2e-registry:5000"
	// dingoSocket is where the operator's dingo driver puts the node socket.
	dingoSocket = "/tmp/node.socket"
)

// TestGouroborosOverlayCanary runs one dingo pool on an image built from
// dingo plus a gouroboros checkout, next to cardano-node pools, and requires
// it to follow their chain with its pool status naming the overlay build.
//
// CONTINUUM_DINGO_SRC and CONTINUUM_GOUROBOROS_SRC name clean checkouts;
// CONTINUUM_OVERLAY_MODE is repro (default) or fast. Without the checkouts the
// test skips: it has nothing to build.
func TestGouroborosOverlayCanary(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 15 minutes")
	}
	dingoSrc, gouroSrc := os.Getenv("CONTINUUM_DINGO_SRC"), os.Getenv("CONTINUUM_GOUROBOROS_SRC")
	if dingoSrc == "" || gouroSrc == "" {
		t.Skip("set CONTINUUM_DINGO_SRC and CONTINUUM_GOUROBOROS_SRC (make e2e DINGO_SRC=... GOUROBOROS_SRC=...)")
	}
	mode := devbuild.Mode(os.Getenv("CONTINUUM_OVERLAY_MODE"))
	if mode == "" {
		mode = devbuild.Repro
	}
	built := buildOverlay(t, dingoSrc, gouroSrc, mode)
	gouroSha := built.Build.Revisions[devbuild.Gouroboros]
	if gouroSha == "" {
		t.Fatalf("pushed image carries no gouroboros revision: %+v", built.Build)
	}

	spec := genesis.Default()
	ns := newNS(t)
	registry := clusterRegistry + "/continuum"
	installOperator(t, ns, "--registry", registry, "--registry-insecure")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Minute)
		defer cancel()
		_, _ = kubectl(ctx, "", "-n", ns, "delete", "continuumpools", "--all", "--wait=true", "--timeout=60s")
		_, _ = kubectl(ctx, "", "-n", ns, "wait", "--for=delete", "pod", "-l", operator.LabelWorkload, "--timeout=100s")
	})
	prepullNodeImage(t, ns)
	kube(t, genesisResources, "-n", ns, "apply", "-f", "-")
	deployGenesis(t, ns, 90*time.Second)
	read := genesisReader(t, ns)
	publishGenesis(t, ns, read, spec)
	pods := applyNetwork(t, ns, spec)

	// The cluster pulls by the in-cluster registry name; the push used the host's.
	ref := clusterRegistry + built.Ref[strings.Index(built.Ref, "/"):]
	dingo := v1alpha1.ContinuumPool{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ContinuumPool"},
		ObjectMeta: metav1.ObjectMeta{Name: "dingo"},
		Spec: v1alpha1.ContinuumPoolSpec{
			NetworkRef: "devnet", PoolID: poolID(spec.Pools), Implementation: v1alpha1.Dingo,
			Tier: v1alpha1.TierValidating, Image: ref,
		},
	}
	apply(t, ns, dingo)

	poll(t, 5*time.Minute, 5*time.Second, "pool status to carry the overlay build", func(ctx context.Context) (bool, error) {
		out, err := kubectl(ctx, "", "-n", ns, "get", "continuumpool", "dingo", "-o", "jsonpath={.status.image}")
		if err != nil || out == "" {
			return false, nil
		}
		var got v1alpha1.ResolvedImage
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			return false, err
		}
		return got.Digest != "", nil
	})
	checkOverlayStatus(t, ns, mode, gouroSha)

	pod := operator.PodName(operator.Name(&dingo))
	start := systemStart(t, read("config/"+genesis.ShelleyFile))
	deadline := start.Add(10 * time.Minute)
	poll(t, time.Until(deadline), 15*time.Second, "dingo to follow the chain", func(ctx context.Context) (bool, error) {
		cn := tips(ctx, ns, pods, spec.Magic)
		dn := dingoTip(ctx, ns, pod, spec.Magic)
		t.Logf("cardano-node tips %+v, dingo tip %+v", cn, dn)
		if len(cn) != len(pods) || dn == nil || dn.Block < 5 {
			return false, nil
		}
		hi := 0
		for _, tp := range cn {
			hi = max(hi, tp.Block)
		}
		return hi-dn.Block <= int(spec.SecurityParam), nil
	})
}

// buildOverlay builds and pushes the overlay image to the host-side registry.
func buildOverlay(t *testing.T, dingoSrc, gouroSrc string, mode devbuild.Mode) v1alpha1.ResolvedImage {
	t.Helper()
	set, err := image.NewSettings(mustEnv(t, "CONTINUUM_REGISTRY"), new(true), os.Getenv, "", image.WithPush())
	if err != nil {
		t.Fatal(err)
	}
	arch := strings.TrimSpace(kube(t, "", "get", "nodes", "-o", "jsonpath={.items[0].status.nodeInfo.architecture}"))
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	b := devbuild.New(devbuild.ExecRunner{}, image.NewResolver(set.Resolver(), log), set, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	got, err := b.Build(ctx, devbuild.Request{
		Dingo: dingoSrc, Mode: mode, Arch: arch,
		With: []devbuild.Overlay{{Module: devbuild.Gouroboros, Path: gouroSrc}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// checkOverlayStatus requires the pool's status.image.build to name the
// overlay: the build mode and the gouroboros revision.
func checkOverlayStatus(t *testing.T, ns string, mode devbuild.Mode, gouroSha string) {
	t.Helper()
	out := kube(t, "", "-n", ns, "get", "continuumpool", "dingo", "-o", "jsonpath={.status.image.build}")
	var b v1alpha1.BuildInfo
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatalf("status.image.build %q: %v", out, err)
	}
	if string(b.Mode) != string(mode) || b.Revisions[devbuild.Gouroboros] != gouroSha {
		t.Fatalf("status.image.build = %+v, want mode %s and gouroboros %s", b, mode, gouroSha)
	}
	if mode == devbuild.Repro && b.Dirty {
		t.Fatalf("repro build reported dirty: %+v", b)
	}
}

// dingoTip queries the dingo pod's tip with the cardano-cli the image ships.
// It is nil while the pod does not answer.
func dingoTip(ctx context.Context, ns, pod string, magic uint32) *tip {
	if pod == "" {
		return nil
	}
	raw, err := kubectl(ctx, "", "-n", ns, "exec", pod, "--",
		"cardano-cli", "query", "tip", "--testnet-magic", strconv.Itoa(int(magic)), "--socket-path", dingoSocket)
	if err != nil {
		return nil
	}
	var tp tip
	if json.Unmarshal([]byte(raw), &tp) != nil {
		return nil
	}
	return &tp
}
