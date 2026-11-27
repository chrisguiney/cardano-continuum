package tla

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed UtxoVectors.tla
var vectorsModule []byte

// Output is one UTxO entry of the abstract ledger.
type Output struct {
	Coin int    `json:"coin"`
	Tok  int    `json:"tok"`
	Lock string `json:"lock"`
}

// Return is a Babbage collateral return output.
type Return struct {
	Coin int `json:"coin"`
	Tok  int `json:"tok"`
}

// Ledger is the abstract ledger state a vector's verdict depends on. UTxO
// lists only the occupied places.
type Ledger struct {
	UTxO       map[string]Output `json:"utxo"`
	Slot       int               `json:"slot"`
	Registered bool              `json:"registered"`
	Reward     int               `json:"reward"`
}

// AbstractTx is a transaction of Utxo.tla: field names follow the model.
type AbstractTx struct {
	Ins    []string `json:"ins"`
	Coll   []string `json:"coll"`
	Refs   []string `json:"refs"`
	Delta  int      `json:"delta"`
	TDelta int      `json:"tdelta"`
	Fee    int      `json:"fee"`
	Mint   int      `json:"mint"`
	Cert   string   `json:"cert"`
	Wdrl   bool     `json:"wdrl"`
	OLock  string   `json:"olock"`
	Big    bool     `json:"big"`
	Lo     int      `json:"lo"`
	Hi     int      `json:"hi"`
	Rdmrs  bool     `json:"rdmrs"`
	Valid  bool     `json:"valid"`
	Pass   bool     `json:"pass"`
	// Ret is the collateral return; nil when the transaction has none.
	Ret   *Return `json:"ret,omitempty"`
	TColl int     `json:"tcoll"`
}

// Vector is one boundary transaction in one ledger state with the verdict of
// the reference rule.
type Vector struct {
	Era         string     `json:"era"`
	Family      string     `json:"family"`
	Ledger      Ledger     `json:"ledger"`
	Tx          AbstractTx `json:"tx"`
	SpecAccepts bool       `json:"specAccepts"`
}

// VectorSet is the export of one era.
type VectorSet struct {
	Era string
	// Distinct is the number of distinct states TLC found in TableSpec.
	Distinct int
	// Counts has one entry per state dumped: TLC's cardinality of each
	// family's expressible transactions in that state. Expressibility reads
	// the UTxO, so the counts differ between states.
	Counts  []map[string]int
	Vectors []Vector
}

// LoadVectors reads JSON Lines vectors. A malformed or unknown-field line is
// an error naming its line number.
func LoadVectors(r io.Reader) ([]Vector, error) {
	var out []Vector
	br := bufio.NewReaderSize(r, 1<<20)
	for n := 1; ; n++ {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var v Vector
			dec := json.NewDecoder(bytes.NewReader(line))
			dec.DisallowUnknownFields()
			if derr := dec.Decode(&v); derr != nil {
				return nil, fmt.Errorf("vectors line %d: %w", n, derr)
			}
			out = append(out, v)
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("vectors line %d: %w", n, err)
		}
	}
}

// WriteVectors writes vectors as JSON Lines.
func WriteVectors(w io.Writer, vs []Vector) error {
	enc := json.NewEncoder(w)
	for i, v := range vs {
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("write vector %d: %w", i, err)
		}
	}
	return nil
}

var (
	eraName  = regexp.MustCompile(`^[A-Za-z]+$`)
	distinct = regexp.MustCompile(`(\d+) distinct states found`)
)

// familyOrder fixes the row order within a state.
var familyOrder = map[string]int{"inputs": 0, "balance": 1, "outputs": 2, "interval": 3, "phase2": 4}

// ExportEra enumerates TableSpec for one era. dir holds Utxo.tla and
// Utxo<era>Table.cfg; the cfg's constants are kept and its invariants are
// replaced by the dumping invariant of the wrapper module (see the package
// documentation).
func ExportEra(ctx context.Context, tools Tools, dir, era string, timeout time.Duration) (VectorSet, error) {
	if !eraName.MatchString(era) {
		return VectorSet{}, fmt.Errorf("bad era name %q", era)
	}
	if err := os.MkdirAll(tools.Tmp, 0o755); err != nil {
		return VectorSet{}, fmt.Errorf("create tmp %s: %w", tools.Tmp, err)
	}
	work, err := os.MkdirTemp(tools.Tmp, "vec-")
	if err != nil {
		return VectorSet{}, fmt.Errorf("create run dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	if err := stage(dir, era, work); err != nil {
		return VectorSet{}, err
	}
	workers := max(tools.Workers, 1)
	out, exit, timedOut, err := runCmd(ctx, timeout, work, tools, filepath.Join(work, "run"), []string{
		"-noGenerateSpecTE", "-workers", strconv.Itoa(workers),
		"-metadir", filepath.Join(work, "states"),
		"-config", "UtxoVectors.cfg", "UtxoVectors.tla",
	})
	if err != nil {
		return VectorSet{}, fmt.Errorf("export %s: %w", era, err)
	}
	if o, _ := Classify(out, exit, timedOut); o != Passed {
		return VectorSet{}, fmt.Errorf("export %s: tlc %s:\n%s", era, o, excerpt(out))
	}
	m := distinct.FindStringSubmatch(out)
	if m == nil {
		return VectorSet{}, fmt.Errorf("export %s: no distinct-state count in tlc output", era)
	}
	n, _ := strconv.Atoi(m[1])
	set, err := readDumps(filepath.Join(work, "vectors"), era)
	if err != nil {
		return VectorSet{}, err
	}
	set.Distinct = n
	return set, nil
}

// stage copies Utxo.tla, writes the wrapper module and a cfg derived from the
// era's table cfg into work.
func stage(dir, era, work string) error {
	tla, err := os.ReadFile(filepath.Join(dir, "Utxo.tla"))
	if err != nil {
		return fmt.Errorf("read Utxo.tla: %w", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "Utxo"+era+"Table.cfg"))
	if err != nil {
		return fmt.Errorf("read table cfg: %w", err)
	}
	var kept []string
	for _, l := range strings.Split(string(cfg), "\n") {
		if strings.HasPrefix(l, "INVARIANT") {
			break
		}
		kept = append(kept, l)
	}
	kept = append(kept, "INVARIANT VecDump", "")
	files := map[string][]byte{
		"Utxo.tla":        tla,
		"UtxoVectors.tla": vectorsModule,
		"UtxoVectors.cfg": []byte(strings.Join(kept, "\n")),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(work, name), b, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(work, "vectors"), 0o755); err != nil {
		return fmt.Errorf("create vectors dir: %w", err)
	}
	return nil
}

type rawTx struct {
	AbstractTx
	Ret Return `json:"ret"`
}

type dumpFile struct {
	State struct {
		UTxO       map[string]Output `json:"utxo"`
		Slot       int               `json:"slot"`
		Registered bool              `json:"registered"`
		Reward     int               `json:"reward"`
	} `json:"state"`
	Counts map[string]int `json:"counts"`
	Rows   []struct {
		Family      string `json:"family"`
		Tx          rawTx  `json:"tx"`
		SpecAccepts bool   `json:"specAccepts"`
	} `json:"rows"`
}

// readDumps decodes the per-state files in name order and orders each state's
// rows, so the result does not depend on TLC's thread scheduling or set order.
func readDumps(dir, era string) (VectorSet, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return VectorSet{}, err
	}
	if len(files) == 0 {
		return VectorSet{}, errors.New("tlc wrote no vector files")
	}
	sort.Strings(files)
	set := VectorSet{Era: era}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return VectorSet{}, fmt.Errorf("read %s: %w", f, err)
		}
		var docs []dumpFile
		if err := json.Unmarshal(b, &docs); err != nil || len(docs) != 1 {
			return VectorSet{}, fmt.Errorf("decode %s: %v", filepath.Base(f), err)
		}
		d := docs[0]
		set.Counts = append(set.Counts, d.Counts)
		set.Vectors = append(set.Vectors, stateVectors(era, d)...)
	}
	return set, nil
}

func stateVectors(era string, d dumpFile) []Vector {
	led := Ledger{UTxO: map[string]Output{}, Slot: d.State.Slot, Registered: d.State.Registered, Reward: d.State.Reward}
	for id, o := range d.State.UTxO {
		if o.Coin >= 0 {
			led.UTxO[id] = o
		}
	}
	type keyed struct {
		v   Vector
		key string
	}
	rows := make([]keyed, 0, len(d.Rows))
	for _, r := range d.Rows {
		tx := r.Tx.AbstractTx
		sort.Strings(tx.Ins)
		sort.Strings(tx.Coll)
		sort.Strings(tx.Refs)
		if r.Tx.Ret.Coin >= 0 {
			ret := r.Tx.Ret
			tx.Ret = &ret
		}
		k, _ := json.Marshal(tx)
		rows = append(rows, keyed{Vector{Era: era, Family: r.Family, Ledger: led, Tx: tx, SpecAccepts: r.SpecAccepts}, string(k)})
	}
	sort.Slice(rows, func(i, j int) bool {
		fi, fj := familyOrder[rows[i].v.Family], familyOrder[rows[j].v.Family]
		if fi != fj {
			return fi < fj
		}
		return rows[i].key < rows[j].key
	})
	out := make([]Vector, len(rows))
	for i, r := range rows {
		out[i] = r.v
	}
	return out
}

// VectorOptions configure ExportVectors.
type VectorOptions struct {
	Repo, Ref string
	CacheDir  string
	// Out receives <Era>.vectors.jsonl per era.
	Out string
	// Eras defaults to every era Utxo.tla models.
	Eras    []string
	Tools   Tools
	Timeout time.Duration
}

// UtxoEras are the eras Utxo.tla covers, oldest first.
var UtxoEras = []string{"Shelley", "Allegra", "Mary", "Alonzo", "Babbage"}

// ExportVectors fetches the model at the pinned ref and writes one vector
// file per era. It returns the row count per era.
func ExportVectors(ctx context.Context, opts VectorOptions) (map[string]int, error) {
	dir, _, err := Fetch(ctx, opts.Repo, opts.Ref, opts.CacheDir)
	if err != nil {
		return nil, err
	}
	eras := opts.Eras
	if len(eras) == 0 {
		eras = UtxoEras
	}
	counts := map[string]int{}
	for _, era := range eras {
		set, err := ExportEra(ctx, opts.Tools, filepath.Join(dir, "specs", "ledger-utxo"), era, opts.Timeout)
		if err != nil {
			return nil, err
		}
		if err := writeVectorFile(filepath.Join(opts.Out, era+".vectors.jsonl"), set.Vectors); err != nil {
			return nil, err
		}
		counts[era] = len(set.Vectors)
	}
	return counts, nil
}

func writeVectorFile(path string, vs []Vector) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	bw := bufio.NewWriter(f)
	if err := WriteVectors(bw, vs); err != nil {
		_ = f.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}

func (r rawTx) abstract() AbstractTx {
	tx := r.AbstractTx
	sort.Strings(tx.Ins)
	sort.Strings(tx.Coll)
	sort.Strings(tx.Refs)
	if r.Ret.Coin >= 0 {
		ret := r.Ret
		tx.Ret = &ret
	}
	return tx
}
