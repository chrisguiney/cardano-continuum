//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/operator"
)

// engineRBAC lets the engine patch its network's ContinuumPools (the pool
// plan) and read their status (pod health).
const engineRBAC = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: continuum-engine
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: continuum-engine
rules:
  - apiGroups: [continuum.blinklabs.io]
    resources: [continuumpools]
    verbs: [get, list, watch, patch]
  - apiGroups: [continuum.blinklabs.io]
    resources: [continuumnetworks]
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: continuum-engine
subjects:
  - kind: ServiceAccount
    name: continuum-engine
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: continuum-engine
`

// TestHonestM3 runs the shipped honest population (about 30 wallets, two
// DReps' worth of civic actors and the four genesis pools' operators) on a
// devnet of three cardano-node pools and one dingo pool, all forging, for
// three epochs. Through the live API it injects an info proposal, the
// committee's hot keys and a go-dark the stake floor must refuse. At the
// end delegations, a DRep registration, a vote, the proposal, a lock and
// an unlock and a mint have settled, the go-dark was refused, no epoch
// went without a block, value is conserved, and every pool is on one chain.
func TestHonestM3(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 35 minutes")
	}
	spec := genesis.Default()
	spec.Pools = 4
	ns, pods, start := bootNetworkWith(t, spec, func(i int) v1alpha1.ContinuumPool {
		p := poolObject(i)
		if i == spec.Pools-1 {
			p.Spec.Implementation = v1alpha1.Dingo
		}
		return p
	})
	p0 := poolObject(0)
	kube(t, engineRBAC, "-n", ns, "apply", "-f", "-")
	deployEngineWith(t, ns, fmt.Sprintf("%s.%s.svc.cluster.local:%d", operator.Name(&p0), ns, operator.NtCPort), true,
		"--profiles=m3", "--wallets=30", "--pools=4", fmt.Sprintf("--genesis-pools=%d", spec.Pools), "--floor=60",
		"--network=devnet", "--namespace="+ns, "--serve=127.0.0.1:8088")

	epoch := time.Duration(spec.EpochLength) * spec.SlotLength
	poll(t, time.Until(start.Add(epoch+time.Minute)), 15*time.Second, "the engine in epoch 1", func(ctx context.Context) (bool, error) {
		p, ok := lastProgress(ctx, ns)
		return ok && p.Epoch >= 1, nil
	})
	api := portForward(t, ns, enginePod, 8088)
	for _, s := range []string{"info_proposal", "hot_keys", "go_dark"} {
		postControl(t, api, fmt.Sprintf(`{"request_id":"%s","op":"inject","scenario":"%s"}`, s, s))
	}
	// The engine refuses an info proposal while no registered staker holds
	// the deposit, and a refusal is not retried, so ask again until one
	// settles; the DReps cannot vote before then.
	reproposed, polls := 0, 0

	var final progress
	poll(t, time.Until(start.Add(3*epoch+6*time.Minute)), 15*time.Second, "the engine past epoch 3", func(ctx context.Context) (bool, error) {
		p, ok := lastProgress(ctx, ns)
		t.Logf("progress: %+v", p)
		final = p
		polls++
		if ok && p.Settled["propose"] == 0 && p.Epoch < 3 && polls%4 == 0 {
			reproposed++
			postControl(t, api, fmt.Sprintf(`{"request_id":"info_proposal_%d","op":"inject","scenario":"info_proposal"}`, reproposed))
		}
		return ok && p.Epoch >= 3 && p.TipEpoch >= 3, nil
	})
	if t.Failed() || final.Settled["propose"] == 0 {
		logFindings(t, api)
	}
	for _, k := range []string{"delegate", "register_drep", "vote", "propose", "lock", "unlock", "mint"} {
		if final.Settled[k] == 0 {
			t.Errorf("no %s settled: %v", k, final.Settled)
		}
	}
	if final.Guarded == 0 {
		t.Error("the stake floor refused no go-dark")
	}
	if final.Incidents != 0 {
		t.Errorf("%d incidents: an epoch passed with no block", final.Incidents)
	}
	if !final.Conserved || final.Supply != final.Genesis {
		t.Errorf("supply %d, genesis %d: value not conserved", final.Supply, final.Genesis)
	}
	if final.DoublePaid != 0 {
		t.Errorf("%d intents confirmed twice", final.DoublePaid)
	}
	dingo := pods[len(pods)-1]
	agreeTips(t, ns, pods[:len(pods)-1], dingo, spec)
}

// agreeTips requires every pool to reach the same tip block within a few
// minutes: a pool on its own fork never does.
func agreeTips(t *testing.T, ns string, nodes []string, dingo string, s genesis.Spec) {
	t.Helper()
	poll(t, 4*time.Minute, 5*time.Second, "every pool on one tip", func(ctx context.Context) (bool, error) {
		ts := tips(ctx, ns, nodes, s.Magic)
		if dingo != "" {
			if d := dingoTip(ctx, ns, dingo, s.Magic); d != nil {
				ts = append(ts, *d)
			}
		}
		t.Logf("tips: %+v", ts)
		want := len(nodes)
		if dingo != "" {
			want++
		}
		if len(ts) != want {
			return false, nil
		}
		for _, tp := range ts[1:] {
			if tp.Hash != ts[0].Hash {
				return false, nil
			}
		}
		return true, nil
	})
}

// portForward forwards a local port kubectl picks to the pod's port until
// the test ends and returns the base URL.
func portForward(t *testing.T, ns, pod string, port int) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	cmd := exec.CommandContext(ctx, "kubectl", "-n", ns, "port-forward", "pod/"+pod, fmt.Sprintf(":%d", port))
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	// The first line is "Forwarding from 127.0.0.1:<port> -> <port>".
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("port-forward: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()
	addr, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "Forwarding from "), " ")
	if !ok {
		t.Fatalf("port-forward said %q", line)
	}
	base := "http://" + addr
	poll(t, time.Minute, 2*time.Second, "the live API through the port-forward", func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if err != nil {
			return false, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, nil
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK, nil
	})
	return base
}

func postControl(t *testing.T, base, body string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/control", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	t.Logf("control %s: HTTP %d %s", body, resp.StatusCode, reply)
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		t.Fatalf("control %s: HTTP %d", body, resp.StatusCode)
	}
}

// logFindings prints what the engine's oracle filed, which names why a
// transaction the engine built was refused or never confirmed.
func logFindings(t *testing.T, base string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/findings?limit=50", nil)
	if err != nil {
		t.Logf("findings: %v", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("findings: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	t.Logf("findings: HTTP %d %s", resp.StatusCode, body)

	sctx, scancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer scancel()
	sreq, err := http.NewRequestWithContext(sctx, http.MethodGet, base+"/api/decisions?kind=Controlled&last_event_id=0", nil)
	if err != nil {
		return
	}
	sresp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		t.Logf("controlled decisions: %v", err)
		return
	}
	defer func() { _ = sresp.Body.Close() }()
	sb, _ := io.ReadAll(io.LimitReader(sresp.Body, 64<<10))
	t.Logf("controlled decisions: %s", sb)
}
