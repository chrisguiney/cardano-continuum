//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
)

// seedHex is a fixed run seed: 32 bytes, hex encoded.
const seedHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// busybox is only used to read files off the genesis volume.
const busybox = "busybox:1.37.0"

const genesisResources = `apiVersion: v1
kind: Secret
metadata:
  name: continuum-seed
stringData:
  seed: ` + seedHex + `
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: continuum-genesis
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 100Mi
`

// Arguments of deploy/genesis/job.yaml that deployGenesis rewrites.
const (
	startDelayArg = "-start-delay=60s"
	poolHostArg   = "-pool-host=pool-%d"
)

// deployGenesis renders the default spec into the continuum-genesis volume of
// ns with deploy/genesis/job.yaml and waits for the Job to finish. The job's
// start delay is replaced by delay. Pool host names are made fully qualified
// within ns: cardano-node resolves topology hosts with its own DNS client,
// which ignores the pod's search domains, so a bare "pool-0" never resolves
// in Kubernetes and the pools never connect to each other.
func deployGenesis(t *testing.T, ns string, delay time.Duration) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "genesis", "job.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	job := string(raw)
	for from, to := range map[string]string{
		"image: continuum-genesis": "image: " + koBuild(t, "continuum-genesis"),
		startDelayArg:              "-start-delay=" + delay.String(),
		poolHostArg:                "-pool-host=pool-%d." + ns + ".svc.cluster.local",
	} {
		if strings.Count(job, from) != 1 {
			t.Fatalf("deploy/genesis/job.yaml: want exactly one %q", from)
		}
		job = strings.Replace(job, from, to, 1)
	}
	kube(t, genesisResources, "-n", ns, "apply", "-f", "-")
	kube(t, job, "-n", ns, "apply", "-f", "-")
	poll(t, 4*time.Minute, 2*time.Second, "genesis job to complete", func(ctx context.Context) (bool, error) {
		out, err := kubectl(ctx, "", "-n", ns, "get", "job", "continuum-genesis", "-o", "json")
		if err != nil {
			return false, err
		}
		var j batchv1.Job
		if err := json.Unmarshal([]byte(out), &j); err != nil {
			return false, err
		}
		for _, c := range j.Status.Conditions {
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

// TestGenesisJob runs the genesis Job from deploy/genesis and reads the
// default spec back from the volume.
func TestGenesisJob(t *testing.T) {
	ns := newNS(t)
	deployGenesis(t, ns, 60*time.Second)

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
	for i := range want.Pools {
		for _, f := range []string{"kes.skey", "vrf.skey", "opcert.cert", "topology.json"} {
			read("pools/" + string(rune('0'+i)) + "/" + f)
		}
	}
}
