//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/genesis/publish"
)

// busybox is only used to read files off the genesis volume.
const busybox = "busybox:1.37.0"

// genesisParts installs the genesis Job alone: no operator, network or engine.
var genesisParts = parts{genesis: true}

// startDelay sets the genesis Job's -start-delay.
func startDelay(d time.Duration) string { return "genesis.startDelay=" + d.String() }

// waitGenesis waits for the chart's genesis Job in ns to complete.
func waitGenesis(t *testing.T, ns string) {
	t.Helper()
	poll(t, 10*time.Minute, 2*time.Second, "genesis job to complete", func(ctx context.Context) (bool, error) {
		out, err := kubectl(ctx, "", "-n", ns, "get", "job", "-l", "app=continuum-genesis", "-o", "json")
		if err != nil {
			return false, err
		}
		var jobs batchv1.JobList
		if err := json.Unmarshal([]byte(out), &jobs); err != nil {
			return false, err
		}
		if len(jobs.Items) != 1 {
			return false, nil
		}
		for _, c := range jobs.Items[0].Status.Conditions {
			if c.Status != corev1.ConditionTrue {
				continue
			}
			switch c.Type {
			case batchv1.JobComplete:
				return true, nil
			case batchv1.JobFailed:
				t.Fatalf("genesis job failed: %s", c.Message)
			}
		}
		return false, nil
	})
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// genesisReader starts a pod that mounts the genesis volume and returns a
// function reading a file from it.
func genesisReader(t *testing.T, ns string) func(path string) string {
	t.Helper()
	const reader = `apiVersion: v1
kind: Pod
metadata:
  name: reader
spec:
  terminationGracePeriodSeconds: 0
  containers:
    - name: main
      image: ` + busybox + `
      command: [sleep, "3600"]
      volumeMounts:
        - {name: genesis, mountPath: /genesis, readOnly: true}
  volumes:
    - name: genesis
      persistentVolumeClaim: {claimName: continuum-genesis}
`
	kube(t, reader, "-n", ns, "apply", "-f", "-")
	kube(t, "", "-n", ns, "wait", "--for=condition=Ready", "--timeout=3m", "pod/reader")
	read := func(path string) string {
		return kube(t, "", "-n", ns, "exec", "reader", "--", "cat", "/genesis/"+path)
	}
	return read
}

// publishedShelley is the Shelley genesis the Job published to ns.
func publishedShelley(t *testing.T, ns string) string {
	t.Helper()
	return kube(t, "", "-n", ns, "get", "configmap", "devnet-genesis", "-o", `jsonpath={.data.shelley-genesis\.json}`)
}

// TestGenesisJob installs the chart's genesis Job alone, reads the default
// spec back from the volume and requires the Job to have published the
// configuration and every pool's keys.
func TestGenesisJob(t *testing.T) {
	ns := newNS(t)
	installChart(t, ns, genesisParts, startDelay(60*time.Second))
	waitGenesis(t, ns)

	read := genesisReader(t, ns)

	if h := strings.TrimSpace(read("spec.sha256")); !hex64.MatchString(h) {
		t.Errorf("spec.sha256 = %q, want 64 hex characters", h)
	}
	var shelley struct {
		EpochLength      int64   `json:"epochLength"`
		SecurityParam    int64   `json:"securityParam"`
		ActiveSlotsCoeff float64 `json:"activeSlotsCoeff"`
		NetworkMagic     uint32  `json:"networkMagic"`
	}
	if err := json.Unmarshal([]byte(read("config/"+genesis.ShelleyFile)), &shelley); err != nil {
		t.Fatal(err)
	}
	want := genesis.Default()
	if shelley.EpochLength != want.EpochLength || shelley.SecurityParam != want.SecurityParam ||
		shelley.ActiveSlotsCoeff != want.ActiveSlotsCoeff || shelley.NetworkMagic != want.Magic {
		t.Errorf("shelley genesis = %+v, want default spec %+v", shelley, want)
	}
	if got := publishedShelley(t, ns); got != read("config/"+genesis.ShelleyFile) {
		t.Error("the published Shelley genesis differs from the volume's")
	}

	pools := 4 // the chart's default pools: three cardano-node and one dingo
	for i := range pools {
		for _, f := range []string{"kes.skey", "vrf.skey", "opcert.cert", "topology.json"} {
			read(fmt.Sprintf("pools/%d/%s", i, f))
		}
		keys := kube(t, "", "-n", ns, "get", "secret", fmt.Sprintf("keys-%d", i), "-o", "jsonpath={.data}")
		for _, f := range publish.KeyFiles {
			if !strings.Contains(keys, `"`+f+`"`) {
				t.Errorf("secret keys-%d lacks %s", i, f)
			}
		}
	}
}
