package genesis

import (
	"encoding/json"
	"fmt"
)

// File names in the config directory, as the node config refers to them.
const (
	ByronFile   = "byron-genesis.json"
	ShelleyFile = "shelley-genesis.json"
	AlonzoFile  = "alonzo-genesis.json"
	ConwayFile  = "conway-genesis.json"
	ConfigFile  = "config.json"
)

// renderNodeConfig is a cardano-node 11 configuration with every era
// transition at epoch 0, so the chain starts in Conway, and the genesis
// hashes pinned. It logs to stdout in machine format with Forge.Loop at Info,
// which is where forged blocks are reported.
func renderNodeConfig(h Hashes) ([]byte, error) {
	cfg := map[string]any{
		"Protocol":                    "Cardano",
		"RequiresNetworkMagic":        "RequiresMagic",
		"ByronGenesisFile":            ByronFile,
		"ByronGenesisHash":            h.Byron,
		"ShelleyGenesisFile":          ShelleyFile,
		"ShelleyGenesisHash":          h.Shelley,
		"AlonzoGenesisFile":           AlonzoFile,
		"AlonzoGenesisHash":           h.Alonzo,
		"ConwayGenesisFile":           ConwayFile,
		"ConwayGenesisHash":           h.Conway,
		"LastKnownBlockVersion-Major": 3,
		"LastKnownBlockVersion-Minor": 1,
		"LastKnownBlockVersion-Alt":   0,
		"LedgerDB":                    map[string]any{"Backend": "V2InMemory"},
		"PeerSharing":                 false,
		"TraceOptions": map[string]any{
			"":           map[string]any{"backends": []string{"Stdout MachineFormat"}, "detail": "DNormal", "severity": "Notice"},
			"Forge.Loop": map[string]any{"severity": "Info"},
			"ChainDB":    map[string]any{"severity": "Info"},
		},
	}
	for _, era := range []string{"Shelley", "Allegra", "Mary", "Alonzo", "Babbage", "Conway"} {
		cfg["Test"+era+"HardForkAtEpoch"] = 0
	}
	return marshal(cfg)
}

type accessPoint struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type localRoot struct {
	AccessPoints []accessPoint `json:"accessPoints"`
	Advertise    bool          `json:"advertise"`
	Trustable    bool          `json:"trustable"`
	Valency      int           `json:"valency"`
}

// renderTopology connects pool self to every other pool as a trusted local
// root, with no public roots and no ledger peers.
func renderTopology(s Spec, self int) ([]byte, error) {
	root := localRoot{AccessPoints: []accessPoint{}, Trustable: true}
	for i := range s.Pools {
		if i == self {
			continue
		}
		root.AccessPoints = append(root.AccessPoints, accessPoint{fmt.Sprintf(s.PoolHost, i), s.PoolPort})
	}
	root.Valency = len(root.AccessPoints)
	return marshal(struct {
		LocalRoots         []localRoot `json:"localRoots"`
		PublicRoots        []struct{}  `json:"publicRoots"`
		UseLedgerAfterSlot int         `json:"useLedgerAfterSlot"`
	}{[]localRoot{root}, []struct{}{}, -1})
}

// marshal is the formatting shared by every rendered file except Byron's:
// four-space indent and a trailing newline. File hashes cover these bytes.
func marshal(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return nil, fmt.Errorf("render json: %w", err)
	}
	return append(b, '\n'), nil
}
