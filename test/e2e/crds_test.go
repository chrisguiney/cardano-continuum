//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"testing"
)

const networkYAML = `apiVersion: continuum.blinklabs.io/v1alpha1
kind: ContinuumNetwork
metadata:
  name: devnet
spec:
  magic: 42
  epochLength: 500
  securityParam: 5
  genesisRef: continuum-genesis
  topology:
    policy: %s
  budget:
    cpuMillis: 4000
    memoryMiB: 8192
    maxPools: 3
`

// TestCRDsInstall applies the chart's CRDs, waits for them to be
// established, accepts a valid ContinuumNetwork and rejects one whose
// topology policy is outside the enum. The CRDs stay: other installs on the
// cluster may hold resources of them.
func TestCRDsInstall(t *testing.T) {
	kube(t, "", "apply", "-f", filepath.Join(repoRoot(t), "charts", "continuum", "crds"))
	for _, crd := range []string{"continuumnetworks", "continuumpools"} {
		kube(t, "", "wait", "--for=condition=Established", "--timeout=60s",
			"crd/"+crd+".continuum.blinklabs.io")
	}

	ns := newNS(t)
	kube(t, fmt.Sprintf(networkYAML, "mesh"), "-n", ns, "apply", "-f", "-")
	got := kube(t, "", "-n", ns, "get", "continuumnetwork", "devnet", "-o", "jsonpath={.spec.topology.policy}")
	if got != "mesh" {
		t.Errorf("stored policy = %q, want mesh", got)
	}
	if _, err := kubectl(t.Context(), fmt.Sprintf(networkYAML, "bogus"), "-n", ns, "apply", "-f", "-"); err == nil {
		t.Error("apply with policy bogus succeeded, want the schema to reject it")
	}
}
