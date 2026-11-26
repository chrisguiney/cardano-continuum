package scenario

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

func provOf(p tla.Provenance, repo, commit string) Provenance {
	return Provenance{Repo: repo, Commit: commit, Spec: p.Spec, Cfg: p.Cfg, Invariant: p.Invariant}
}

// FromUtxo turns one exported Utxo.tla configuration into scenarios: a ledger
// trace or a walk is one scenario of several steps; a table violation is one
// single-step scenario per witness, at most maxRows of them.
func FromUtxo(ex tla.UtxoExport, source Source, repo, commit string, maxRows int) []Scenario {
	base := Scenario{
		Version: FormatVersion, Family: FamilyUtxo, Source: source,
		Prov: provOf(ex.Prov, repo, commit),
	}
	mk := func(id string, steps []tla.Verdict) Scenario {
		s := base
		s.ID = id
		s.Utxo = &UtxoCase{Era: ex.Era, Mutation: ex.Mutation, Start: ex.Start, Steps: steps}
		return s
	}
	if len(ex.Run) > 0 {
		return []Scenario{mk(ex.Name, ex.Run)}
	}
	var out []Scenario
	for i, row := range ex.Rows[:min(len(ex.Rows), maxRows)] {
		out = append(out, mk(fmt.Sprintf("%s/%d", ex.Name, i), []tla.Verdict{row}))
	}
	return out
}

// FromWitnesses turns the cells behind a Witnesses.tla violation into one
// scenario each, at most maxCells of them.
func FromWitnesses(ex tla.WitnessExport, repo, commit string, maxCells int) []Scenario {
	var out []Scenario
	for i, c := range ex.Cells[:min(len(ex.Cells), maxCells)] {
		out = append(out, Scenario{
			Version: FormatVersion, ID: fmt.Sprintf("%s/%d", ex.Name, i),
			Family: FamilyWitnesses, Source: SourceTrace, Prov: provOf(ex.Prov, repo, commit),
			Witness: &WitnessCase{Era: ex.Era, Mutation: ex.Mutation, Env: ex.Env, Kind: c.Kind, Cell: c.Cell, Accepts: c.Accepts},
		})
	}
	return out
}

// FromCodecSeed turns a harvested cbor-codec, ledger-codec or txsize-framing
// seed into a scenario: the counterexample's last state is the table input.
// Models keep the input in a variable named input, or spread over the
// state's variables.
func FromCodecSeed(seed tla.Seed, repo, commit string) (Scenario, error) {
	if seed.Trace == nil || len(seed.Trace.Steps) == 0 {
		return Scenario{}, fmt.Errorf("%s: seed has no trace", seed.Name)
	}
	state := seed.Trace.Steps[len(seed.Trace.Steps)-1].State
	var in tla.Value = state
	if v, ok := state["input"]; ok {
		in = v
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return Scenario{}, fmt.Errorf("%s: encode input: %w", seed.Name, err)
	}
	_, name, _ := strings.Cut(seed.Name, "/")
	return Scenario{
		Version: FormatVersion, ID: FamilyCodec + "/" + name, Family: FamilyCodec, Source: SourceTrace,
		Prov:  provOf(seed.Prov, repo, commit),
		Codec: &CodecCase{Model: strings.TrimSuffix(seed.Prov.Spec, ".tla"), Input: raw},
	}, nil
}

// FromVector turns one row of the boundary table into a single-step scenario.
func FromVector(v tla.Vector, n int, repo, commit string) Scenario {
	return Scenario{
		Version: FormatVersion, ID: fmt.Sprintf("%s/vectors/%s/%s-%d", FamilyUtxo, v.Era, v.Family, n),
		Family: FamilyUtxo, Source: SourceVector,
		Prov: Provenance{Repo: repo, Commit: commit, Spec: "Utxo.tla", Cfg: "Utxo" + v.Era + "Table.cfg", Invariant: "SpecAccepts"},
		Utxo: &UtxoCase{Era: v.Era, Start: v.Ledger, Steps: []tla.Verdict{{Tx: v.Tx, Accepts: v.SpecAccepts}}},
	}
}

// SampleVectors reads a JSON Lines vector file and returns n rows chosen
// uniformly, as scenarios in file order. The file is read once and only the
// chosen lines are decoded: the table is too large to hold.
func SampleVectors(r io.Reader, n int, rnd *rand.Rand, repo, commit string) ([]Scenario, error) {
	type pick struct {
		line int
		raw  []byte
	}
	var keep []pick
	br := bufio.NewReaderSize(r, 1<<20)
	for line := 0; ; line++ {
		raw, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			// Reservoir sampling: line k replaces a kept one with
			// probability n/(k+1).
			if len(keep) < n {
				keep = append(keep, pick{line, raw})
			} else if j := rnd.IntN(line + 1); j < n {
				keep[j] = pick{line, raw}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("vectors line %d: %w", line+1, err)
		}
	}
	out := make([]Scenario, 0, len(keep))
	for _, p := range keep {
		vs, err := tla.LoadVectors(bytes.NewReader(p.raw))
		if err != nil || len(vs) != 1 {
			return nil, fmt.Errorf("vectors line %d: %v", p.line+1, err)
		}
		out = append(out, FromVector(vs[0], p.line, repo, commit))
	}
	sortByID(out)
	return out, nil
}

// CodecTable enumerates the codec models' inputs that have a transaction
// form: every MultiAssetQuantity field, value and encoding, every
// PreConwaySetTag field and tag, every PlutusConstrFields shape, the tag 258
// contents and the three framings.
func CodecTable(repo, commit string) []Scenario {
	var out []Scenario
	add := func(model, spec, name string, input any) {
		raw, _ := json.Marshal(input)
		out = append(out, Scenario{
			Version: FormatVersion, ID: fmt.Sprintf("%s/table/%s/%s", FamilyCodec, model, name),
			Family: FamilyCodec, Source: SourceTable,
			Prov:  Provenance{Repo: repo, Commit: commit, Spec: spec},
			Codec: &CodecCase{Model: model, Input: raw},
		})
	}
	for _, f := range []string{"outputAsset", "mintAsset", "coinBare", "coinInPair"} {
		for _, v := range []string{"zero", "five", "w64max", "i64maxPlus1", "minusOne", "i64min", "i64minMinus1", "beyond64"} {
			for _, e := range []string{"head", "bignum", "bignumLongTag", "bignumBehindDup"} {
				if e == "head" && v == "beyond64" || e == "bignumBehindDup" && strings.HasPrefix(f, "coin") {
					continue // MultiAssetQuantity.tla Encodable
				}
				add("MultiAssetQuantity", "cbor-codec/MultiAssetQuantity.tla", f+"-"+v+"-"+e,
					quantityInput{PV: targetMajor, Field: f, Value: v, Enc: e})
			}
		}
	}
	for _, f := range []string{"inputs", "collateral", "requiredSigners", "referenceInputs", "witness"} {
		for _, tag := range []string{"none", "258", "other"} {
			add("PreConwaySetTag", "ledger-codec/PreConwaySetTag.tla", f+"-"+tag, setTagInput{Era: targetEra, Field: f, Tag: tag})
		}
	}
	for _, fam := range []string{"121-127", "1280-1400", "102"} {
		for _, alt := range []string{"uint", "negint"} {
			for _, fields := range []string{"defList", "indefList", "uint", "bytes", "map", "text", "taggedList"} {
				add("PlutusConstrFields", "cbor-codec/PlutusConstrFields.tla", fam+"-"+alt+"-"+fields,
					constrInput{Family: fam, Alt: alt, Fields: fields})
			}
		}
	}
	for _, c := range []string{"defList", "indefList", "uint", "map"} {
		add("ValueSetMembers", "cbor-codec/ValueSetMembers.tla", c, setContentInput{Where: "top", Content: c, Members: []string{}})
	}
	for _, f := range []string{"definite", "wide", "indefinite"} {
		add("TxSizeFraming", "ledger-codec/TxSizeFraming.tla", f, framingInput{Era: targetEra, Framing: f})
	}
	return out
}
