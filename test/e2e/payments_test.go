//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
)

const enginePod = "continuum-engine-0"

// progress is the engine's periodic progress record (internal/cli/live).
type progress struct {
	Msg        string `json:"msg"`
	Epoch      uint64 `json:"epoch"`
	Slot       uint64 `json:"slot"`
	TipSlot    uint64 `json:"tip_slot"`
	Payments   int    `json:"payments"`
	Refills    int    `json:"refills"`
	Confirmed  int    `json:"confirmed"`
	Paid       int    `json:"paid"`
	Released   int    `json:"released"`
	DoublePaid int    `json:"double_paid"`
	Supply     uint64 `json:"supply"`
	Genesis    uint64 `json:"genesis"`
	Conserved  bool   `json:"conserved"`
	TipEpoch   uint64 `json:"tip_epoch"`
	// Settled counts confirmed intents by kind; Guarded the stake floor's
	// refusals; Incidents the stalls.
	Settled   map[string]int `json:"settled"`
	Guarded   int            `json:"guarded"`
	Incidents int            `json:"incidents"`
}

// started is the engine's start record (internal/engine Runner).
type started struct {
	Msg    string `json:"msg"`
	Covers uint64 `json:"covers"`
	LogSeq uint64 `json:"log_seq"`
}

// TestPayments enables the chart's engine against pool 0's
// node-to-client bridge with 20 wallets that the genesis faucet funds, and
// lets them pay each other for two epochs. Mid-run the engine pod is
// deleted; the replacement must resume from its store and keep paying. At
// the end payments are confirmed, the engine's supply equals the faucet's
// genesis output, no intent was confirmed twice, and every pool is on the
// same tip: an engine following a pool on its own fork could pass the rest.
func TestPayments(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 25 minutes")
	}
	spec := genesis.Default()
	b := bootNetwork(t, spec, 0)
	ns, pods, start := b.ns, b.pods, b.start
	// The engine's own defaults for profiles and scale, with no adversaries.
	deployEngine(t, b.release, "engine.profiles=m1", "engine.wallets=20", "engine.scale=1", "engine.attacks=[]")

	epoch := time.Duration(spec.EpochLength) * spec.SlotLength
	var before progress
	poll(t, time.Until(start.Add(epoch+epoch/2)), 15*time.Second, "a confirmed payment", func(ctx context.Context) (bool, error) {
		p, ok := lastProgress(ctx, ns)
		t.Logf("progress: %+v", p)
		before = p
		return ok && p.Paid > 0, nil
	})

	// A normal delete: SIGTERM, then the StatefulSet makes a new pod on the
	// same PVC only once the old one is gone.
	kube(t, "", "-n", ns, "delete", "pod", enginePod, "--wait=true", "--timeout=2m")
	poll(t, 3*time.Minute, 5*time.Second, "the engine pod to come back", func(ctx context.Context) (bool, error) {
		_, err := kubectl(ctx, "", "-n", ns, "wait", "--for=condition=Ready", "--timeout=5s", "pod/"+enginePod)
		return err == nil, nil
	})

	var final progress
	poll(t, time.Until(start.Add(2*epoch+6*time.Minute)), 15*time.Second, "the engine past epoch 2", func(ctx context.Context) (bool, error) {
		p, ok := lastProgress(ctx, ns)
		t.Logf("progress: %+v", p)
		final = p
		return ok && p.Epoch >= 2 && p.Paid > before.Paid, nil
	})

	logs := kube(t, "", "-n", ns, "logs", enginePod)
	st, ok := firstRecord[started](logs, "engine started")
	t.Logf("restart: %+v; before %+v; final %+v", st, before, final)
	if !ok || st.Covers == 0 {
		t.Errorf("the restarted engine did not resume from its store: %+v", st)
	}
	if final.Paid == 0 || final.Paid <= before.Paid {
		t.Errorf("payments confirmed: %d before the restart, %d at the end", before.Paid, final.Paid)
	}
	if !final.Conserved || final.Supply != final.Genesis {
		t.Errorf("supply %d, genesis %d: value not conserved", final.Supply, final.Genesis)
	}
	if final.DoublePaid != 0 {
		t.Errorf("%d intents confirmed twice", final.DoublePaid)
	}
	agreeTips(t, ns, pods, "", spec)
}

// deployEngine enables the chart's engine in r with sets and waits for its
// pod to be ready.
func deployEngine(t *testing.T, r *release, sets ...string) {
	t.Helper()
	r.upgrade(t, append([]string{"engine.enabled=true"}, sets...)...)
	poll(t, 5*time.Minute, 5*time.Second, "the engine pod to be ready", func(ctx context.Context) (bool, error) {
		_, err := kubectl(ctx, "", "-n", r.ns, "wait", "--for=condition=Ready", "--timeout=5s", "pod/"+enginePod)
		return err == nil, nil
	})
}

// lastProgress is the engine pod's latest progress record.
func lastProgress(ctx context.Context, ns string) (progress, bool) {
	logs, err := kubectl(ctx, "", "-n", ns, "logs", enginePod)
	if err != nil {
		return progress{}, false
	}
	var last progress
	found := false
	for _, line := range strings.Split(logs, "\n") {
		var p progress
		if json.Unmarshal([]byte(line), &p) == nil && p.Msg == "progress" {
			last, found = p, true
		}
	}
	return last, found
}

// firstRecord decodes the first JSON log line whose msg is msg.
func firstRecord[T any](logs, msg string) (T, bool) {
	var zero T
	for _, line := range strings.Split(logs, "\n") {
		var head struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &head) != nil || head.Msg != msg {
			continue
		}
		var v T
		if json.Unmarshal([]byte(line), &v) == nil {
			return v, true
		}
	}
	return zero, false
}
