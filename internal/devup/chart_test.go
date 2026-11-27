package devup

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// chartValues is the part of charts/continuum/values.yaml that copies a Go
// constant. Helm cannot import Go, so the two are kept in step here.
type chartValues struct {
	Images struct {
		Registry string `json:"registry"`
	} `json:"images"`
	NodeImages struct {
		CardanoNode string `json:"cardanoNode"`
		Dingo       string `json:"dingo"`
	} `json:"nodeImages"`
	Network struct {
		Magic         uint32 `json:"magic"`
		EpochLength   int64  `json:"epochLength"`
		SecurityParam int64  `json:"securityParam"`
		Budget        struct {
			MaxPools  int32 `json:"maxPools"`
			CPUMillis int64 `json:"cpuMillis"`
			MemoryMiB int64 `json:"memoryMiB"`
		} `json:"budget"`
	} `json:"network"`
	Engine struct {
		Attacks []string `json:"attacks"`
	} `json:"engine"`
}

func TestChartDefaults(t *testing.T) {
	raw, err := os.ReadFile("../../charts/continuum/values.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var v chartValues
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}

	g := genesis.Default()
	attacks, err := Attacks("")
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"images.registry", v.Images.Registry, ident.ImageRepo},
		{"nodeImages.cardanoNode", v.NodeImages.CardanoNode, render.CardanoNode{}.Image()},
		{"nodeImages.dingo", v.NodeImages.Dingo, render.Dingo{}.Image()},
		{"network.magic", v.Network.Magic, g.Magic},
		{"network.epochLength", v.Network.EpochLength, g.EpochLength},
		{"network.securityParam", v.Network.SecurityParam, g.SecurityParam},
		{"network.budget.maxPools", v.Network.Budget.MaxPools, int32(v1alpha1.DefaultMaxPools)},
		{"network.budget.cpuMillis", v.Network.Budget.CPUMillis, int64(v1alpha1.DefaultCPUMillis)},
		{"network.budget.memoryMiB", v.Network.Budget.MemoryMiB, int64(v1alpha1.DefaultMemoryMiB)},
		{"engine.attacks", strings.Join(v.Engine.Attacks, ","), attacks.Engine},
	} {
		if c.got != c.want {
			t.Errorf("values.yaml %s = %v, want %v", c.name, c.got, c.want)
		}
	}
}
