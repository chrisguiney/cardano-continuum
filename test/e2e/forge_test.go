//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
)

// nodeImage is the cardano-node image the pool pods run.
const nodeImage = "ghcr.io/blinklabs-io/cardano-node:11.1.3-1"

// poolYAML is pool %[1]d: a Service so the topology's host name resolves, and
// a cardano-node pod that mounts the genesis volume read-only. The pod sets no
// fsGroup, which would widen the key files' mode past what cardano-node
// accepts.
const poolYAML = `apiVersion: v1
kind: Service
metadata:
  name: pool-%[1]d
spec:
  clusterIP: None
  selector: {pool: "%[1]d"}
  ports: [{port: 3001}]
---
apiVersion: v1
kind: Pod
metadata:
  name: pool-%[1]d
  labels: {pool: "%[1]d"}
spec:
  terminationGracePeriodSeconds: 5
  securityContext: {runAsUser: 65532, runAsGroup: 65532}
  containers:
    - name: node
      image: ` + nodeImage + `
      command: [cardano-node, run,
        --config, /config/config.json, --topology, /keys/topology.json,
        --database-path, /data/db, --socket-path, /data/node.socket,
        --host-addr, 0.0.0.0, --port, "3001",
        --shelley-kes-key, /keys/kes.skey, --shelley-vrf-key, /keys/vrf.skey,
        --shelley-operational-certificate, /keys/opcert.cert]
      volumeMounts:
        - {name: genesis, mountPath: /config, subPath: config, readOnly: true}
        - {name: genesis, mountPath: /keys, subPath: "pools/%[1]d", readOnly: true}
        - {name: data, mountPath: /data}
  volumes:
    - name: genesis
      persistentVolumeClaim: {claimName: continuum-genesis}
    - name: data
      emptyDir: {}
`

// prepullYAML binds the genesis volume to a node and pulls nodeImage there
// before the Job fixes systemStart, so the pools do not start late.
const prepullYAML = `apiVersion: v1
kind: Pod
metadata:
  name: prepull
spec:
  terminationGracePeriodSeconds: 0
  containers:
    - name: main
      image: ` + nodeImage + `
      command: [sleep, "3600"]
      volumeMounts:
        - {name: genesis, mountPath: /genesis}
  volumes:
    - name: genesis
      persistentVolumeClaim: {claimName: continuum-genesis}
`

// epochsWanted is how many epochs the chain must pass; two crosses two
// epoch boundaries.
const epochsWanted = 2

// TestDefaultGenesisForges runs three cardano-node pools on the default
// genesis spec and requires every pool to forge blocks in every epoch up to
// epoch 2, so that the epoch boundaries (stake snapshot, nonce evolution)
// are exercised. It takes about 20 minutes.
func TestDefaultGenesisForges(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 20 minutes")
	}
	spec := genesis.Default()
	ns := newNS(t)
	kube(t, genesisResources, "-n", ns, "apply", "-f", "-")
	kube(t, prepullYAML, "-n", ns, "apply", "-f", "-")
	kube(t, "", "-n", ns, "wait", "--for=condition=Ready", "--timeout=8m", "pod/prepull")
	kube(t, "", "-n", ns, "delete", "pod", "prepull", "--wait=false")
	deployGenesis(t, ns, 60*time.Second)
	for i := range spec.Pools {
		kube(t, fmt.Sprintf(poolYAML, i), "-n", ns, "apply", "-f", "-")
	}
	for i := range spec.Pools {
		kube(t, "", "-n", ns, "wait", "--for=condition=Ready", "--timeout=5m", fmt.Sprintf("pod/pool-%d", i))
	}

	start := systemStart(t, kube(t, "", "-n", ns, "exec", "pool-0", "--", "cat", "/config/"+genesis.ShelleyFile))
	deadline := start.Add(time.Duration(epochsWanted)*time.Duration(spec.EpochLength)*spec.SlotLength + 4*time.Minute)
	poll(t, time.Until(deadline), 15*time.Second, "all pools past epoch 2", func(ctx context.Context) (bool, error) {
		tips := tips(ctx, ns, poolPods(spec), spec.Magic)
		t.Logf("tips: %+v", tips)
		if len(tips) != spec.Pools {
			return false, nil
		}
		for _, tp := range tips {
			if tp.Epoch < epochsWanted {
				return false, nil
			}
		}
		return true, nil
	})
	forged := 0
	for _, pod := range poolPods(spec) {
		forged += checkForged(t, ns, pod, spec, epochsWanted)
	}
	checkOneChain(t, tips(t.Context(), ns, poolPods(spec), spec.Magic), forged, spec)
}

// checkOneChain requires the pools to share one chain. Pools that never
// connect each forge their own fork, so a tip far below the number of blocks
// forged is the signature of a partitioned network; a few blocks lost to slot
// battles are expected.
func checkOneChain(t *testing.T, tips []tip, forged int, s genesis.Spec) {
	t.Helper()
	lo, hi := tips[0].Block, tips[0].Block
	for _, tp := range tips {
		lo, hi = min(lo, tp.Block), max(hi, tp.Block)
	}
	t.Logf("tip blocks %d..%d, forged %d", lo, hi, forged)
	if hi-lo > int(s.SecurityParam) {
		t.Errorf("tips differ by %d blocks, more than k=%d", hi-lo, s.SecurityParam)
	}
	if float64(hi) < 0.8*float64(forged) {
		t.Errorf("chain has %d blocks but pools forged %d: the pools are forking apart", hi, forged)
	}
}

type tip struct {
	Epoch int    `json:"epoch"`
	Slot  int    `json:"slot"`
	Block int    `json:"block"`
	Era   string `json:"era"`
}

// poolPods names the pods of the hand-made pools.
func poolPods(s genesis.Spec) []string {
	out := make([]string, s.Pools)
	for i := range out {
		out[i] = fmt.Sprintf("pool-%d", i)
	}
	return out
}

// tips returns the tip of every pod that answers; a pod still starting is
// left out.
func tips(ctx context.Context, ns string, pods []string, magic uint32) []tip {
	var out []tip
	for _, pod := range pods {
		raw, err := kubectl(ctx, "", "-n", ns, "exec", pod, "--",
			"cardano-cli", "query", "tip", "--testnet-magic", strconv.Itoa(int(magic)),
			"--socket-path", "/data/node.socket")
		if err != nil {
			continue
		}
		var tp tip
		if json.Unmarshal([]byte(raw), &tp) == nil {
			out = append(out, tp)
		}
	}
	return out
}

// systemStart reads slot 0's time from a shelley genesis file.
func systemStart(t *testing.T, shelley string) time.Time {
	t.Helper()
	var g struct {
		SystemStart time.Time `json:"systemStart"`
	}
	if err := json.Unmarshal([]byte(shelley), &g); err != nil {
		t.Fatal(err)
	}
	return g.SystemStart
}

var slotRe = regexp.MustCompile(`"slot":\s*(\d+)`)

// checkForged requires pod to have forged in each of the first epochs epochs
// and returns how many blocks it forged. NoLedgerView lines are logged: they are the failure the
// default spec was suspected of.
func checkForged(t *testing.T, ns, pod string, s genesis.Spec, epochs int) int {
	t.Helper()
	logs := kube(t, "", "-n", ns, "logs", pod)
	perEpoch := make([]int, epochs)
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "ForgedBlock") {
			continue
		}
		m := slotRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		slot, _ := strconv.ParseInt(m[1], 10, 64)
		if e := slot / s.EpochLength; e < int64(epochs) {
			perEpoch[e]++
		}
	}
	t.Logf("%s: forged per epoch %v, NoLedgerView lines %d", pod, perEpoch, strings.Count(logs, "NoLedgerView"))
	for e, n := range perEpoch {
		if n == 0 {
			t.Errorf("%s forged nothing in epoch %d", pod, e)
		}
	}
	total := 0
	for _, n := range perEpoch {
		total += n
	}
	return total
}
