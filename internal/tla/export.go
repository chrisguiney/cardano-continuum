package tla

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed UtxoWitness.tla
var utxoWitnessModule []byte

//go:embed UtxoWalk.tla
var utxoWalkModule []byte

//go:embed WitnessesWitness.tla
var witnessesWitnessModule []byte

// Verdict is one abstract transaction and whether the reference accepts it in
// the ledger it was offered to.
type Verdict struct {
	Tx      AbstractTx `json:"tx"`
	Accepts bool       `json:"accepts"`
	// After is the ledger once the step is over, for a transaction offered
	// in sequence; nil for an independent table row.
	After *Ledger `json:"after,omitempty"`
}

// UtxoExport is what one Utxo.tla configuration yields once the transactions
// the model keeps under quantifiers are written out.
type UtxoExport struct {
	Seed
	Era      string
	Mutation string
	// Start is the ledger the transactions are offered to.
	Start Ledger
	// Rows are independent transactions, each offered to Start: the cells
	// that make a table invariant false.
	Rows []Verdict
	// Run is a sequence: each transaction is offered to the ledger the one
	// before it left.
	Run []Verdict
}

// WitEnv is the environment of a Witnesses.tla cell: which keys have a
// witness and the validity bounds, -1 when absent.
type WitEnv struct {
	Wits []string `json:"wits"`
	Lo   int      `json:"lo"`
	Hi   int      `json:"hi"`
}

// WitCell is one cell of Witnesses.tla with the reference's verdict. Kind is
// the family: key, native, script, datum or integrity. Cell is the model's
// record for that family.
type WitCell struct {
	Kind    string          `json:"kind"`
	Cell    json.RawMessage `json:"cell"`
	Accepts bool            `json:"accepts"`
}

// WitnessExport is what one Witnesses.tla Bug configuration yields.
type WitnessExport struct {
	Seed
	Era      string
	Mutation string
	Env      WitEnv
	Cells    []WitCell
}

// wrapper describes how a model family's Bug configurations are rewritten to
// run under a wrapper module.
type wrapper struct {
	model  string // the model module, copied beside the wrapper
	name   string // the wrapper module
	text   []byte
	specs  map[string]string // SPECIFICATION name to its replacement
	traced []string          // invariants with a Wit<Name> twin
}

var utxoWrapper = wrapper{
	model: "Utxo.tla", name: "UtxoWitness.tla", text: utxoWitnessModule,
	specs: map[string]string{"Spec": "WSpec", "TableSpec": "WTableSpec"},
	traced: []string{
		"GoAgrees", "GoBadInputsComplete", "GoMissingInputIsBadInputs",
		"ProbeNoTokenOutputBelowMin", "ProbeNoCollateralAccepted",
		"ProbeNoReturnAccepted", "ProbeNoOverlapAccepted",
	},
}

var witnessesWrapper = wrapper{
	model: "Witnesses.tla", name: "WitnessesWitness.tla", text: witnessesWitnessModule,
	specs: map[string]string{"TableSpec": "TableSpec"},
	traced: []string{
		"GoAgrees", "ProbeNoBootstrapAuthorizes", "ProbeNoOwnerExemption",
		"ProbeNoTimelockAccepted", "ProbeNoRefScriptAccepted",
		"ProbeNoInlineDatumAccepted",
	},
}

var (
	specDecl  = regexp.MustCompile(`^SPECIFICATION\s+(\S+)\s*$`)
	constDecl = regexp.MustCompile(`^\s*(\w+)\s*=\s*"([^"]*)"\s*$`)
	sectionAt = regexp.MustCompile(`^[A-Z_]+\b`)
)

// rewriteCfg points a Bug configuration at the wrapper: the specification is
// replaced by its wrapped twin, and each invariant that has a witness-dumping
// twin gets it listed first, so the witnesses are written in the state where
// the invariant then fails. It returns the rewritten text and the string
// constants the configuration sets.
func (w wrapper) rewriteCfg(cfg string) (string, map[string]string, error) {
	consts := map[string]string{}
	var out []string
	section, sawSpec := "", false
	for _, line := range strings.Split(cfg, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := specDecl.FindStringSubmatch(line); m != nil {
			to, ok := w.specs[m[1]]
			if !ok {
				return "", nil, fmt.Errorf("specification %s has no wrapped form", m[1])
			}
			out, sawSpec, section = append(out, "SPECIFICATION "+to), true, ""
			continue
		}
		if sectionAt.MatchString(line) {
			section = strings.Fields(line)[0]
		}
		if m := constDecl.FindStringSubmatch(line); m != nil && strings.HasPrefix(section, "CONSTANT") {
			consts[m[1]] = m[2]
		}
		if strings.HasPrefix(section, "INVARIANT") && slices.Contains(w.traced, trimmed) {
			out = append(out, "    Wit"+trimmed)
		}
		out = append(out, line)
	}
	if !sawSpec {
		return "", nil, errors.New("no SPECIFICATION line")
	}
	return strings.Join(out, "\n"), consts, nil
}

// staged is one wrapped TLC run's result.
type staged struct {
	run    Run
	consts map[string]string
	// witness is the witness file's content, nil when none was written.
	witness []byte
}

// runWrapped stages the model, the wrapper and the rewritten cfg in a scratch
// directory and runs TLC there.
func (w wrapper) runWrapped(ctx context.Context, tools Tools, dir, cfg string, timeout time.Duration) (staged, error) {
	if cfg != filepath.Base(cfg) {
		return staged{}, fmt.Errorf("bad cfg name %q", cfg)
	}
	raw, err := os.ReadFile(filepath.Join(dir, cfg))
	if err != nil {
		return staged{}, fmt.Errorf("read %s: %w", cfg, err)
	}
	text, consts, err := w.rewriteCfg(string(raw))
	if err != nil {
		return staged{}, fmt.Errorf("%s: %w", cfg, err)
	}
	work, cleanup, err := w.stage(tools, dir, map[string][]byte{"wrapped.cfg": []byte(text)})
	if err != nil {
		return staged{}, err
	}
	defer cleanup()
	run, err := RunTLC(ctx, tools, work, w.name, "wrapped.cfg", timeout)
	if err != nil {
		return staged{}, err
	}
	st := staged{run: run, consts: consts}
	if b, err := os.ReadFile(filepath.Join(work, "witness.json")); err == nil {
		st.witness = b
	}
	return st, nil
}

// stage copies the model and the wrapper, with extra files, into a fresh
// directory under tools.Tmp.
func (w wrapper) stage(tools Tools, dir string, extra map[string][]byte) (string, func(), error) {
	model, err := os.ReadFile(filepath.Join(dir, w.model))
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", w.model, err)
	}
	if err := os.MkdirAll(tools.Tmp, 0o755); err != nil {
		return "", nil, fmt.Errorf("create tmp %s: %w", tools.Tmp, err)
	}
	work, err := os.MkdirTemp(tools.Tmp, "wrap-")
	if err != nil {
		return "", nil, fmt.Errorf("create run dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(work) }
	files := map[string][]byte{w.model: model, w.name: w.text}
	for name, b := range extra {
		files[name] = b
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(work, name), b, 0o644); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("write %s: %w", name, err)
		}
	}
	return work, cleanup, nil
}

func (st staged) seed(family, model, cfg string) Seed {
	seed := Seed{
		Name:   family + "/" + strings.TrimSuffix(cfg, ".cfg"),
		Status: st.run.Outcome,
		Prov: Provenance{
			Family: family, Spec: model, Cfg: cfg,
			Invariant: st.run.Invariant, TLC: st.run.Version,
		},
		Trace: st.run.Trace,
	}
	if st.run.Outcome != Violation {
		seed.Detail = excerpt(st.run.Output)
	}
	return seed
}

// ExportUtxo runs one Utxo.tla Bug configuration of dir under the witness
// wrapper. A table configuration yields Rows; a ledger configuration yields
// Run. A configuration that does not end in a violation yields neither: read
// Status.
func ExportUtxo(ctx context.Context, tools Tools, dir, cfg string, timeout time.Duration) (UtxoExport, error) {
	st, err := utxoWrapper.runWrapped(ctx, tools, dir, cfg, timeout)
	if err != nil {
		return UtxoExport{}, err
	}
	ex := UtxoExport{
		Seed: st.seed("ledger-utxo", utxoWrapper.model, cfg),
		Era:  st.consts["Era"], Mutation: st.consts["Mutation"],
	}
	if st.run.Outcome != Violation {
		return ex, nil
	}
	if st.witness != nil {
		return tableExport(ex, st.witness)
	}
	return runExport(ex)
}

type witnessFile struct {
	Invariant string `json:"invariant"`
	State     struct {
		UTxO       map[string]Output `json:"utxo"`
		Slot       int               `json:"slot"`
		Registered bool              `json:"registered"`
		Reward     int               `json:"reward"`
	} `json:"state"`
	Rows []struct {
		Tx          rawTx `json:"tx"`
		SpecAccepts bool  `json:"specAccepts"`
	} `json:"rows"`
}

func tableExport(ex UtxoExport, raw []byte) (UtxoExport, error) {
	var docs []witnessFile
	if err := json.Unmarshal(raw, &docs); err != nil || len(docs) != 1 {
		return UtxoExport{}, fmt.Errorf("%s: decode witnesses: %v", ex.Name, err)
	}
	d := docs[0]
	if d.Invariant != ex.Prov.Invariant {
		return UtxoExport{}, fmt.Errorf("%s: witnesses are for %s, violation is of %s", ex.Name, d.Invariant, ex.Prov.Invariant)
	}
	ex.Start = Ledger{UTxO: live(d.State.UTxO), Slot: d.State.Slot, Registered: d.State.Registered, Reward: d.State.Reward}
	for _, r := range d.Rows {
		ex.Rows = append(ex.Rows, Verdict{Tx: r.Tx.abstract(), Accepts: r.SpecAccepts})
	}
	sortVerdicts(ex.Rows)
	return ex, nil
}

// runExport reads the applied transactions out of a ledger trace's history
// variable.
func runExport(ex UtxoExport) (UtxoExport, error) {
	steps := ex.Trace.Steps
	start, err := ledgerOf(steps[0].State)
	if err != nil {
		return UtxoExport{}, fmt.Errorf("%s: state 1: %w", ex.Name, err)
	}
	ex.Start = start
	for _, s := range steps[1:] {
		v, ok, err := lastOf(s.State)
		if err != nil {
			return UtxoExport{}, fmt.Errorf("%s: state %d: %w", ex.Name, s.Num, err)
		}
		if ok {
			ex.Run = append(ex.Run, v)
		}
	}
	return ex, nil
}

func live(u map[string]Output) map[string]Output {
	out := map[string]Output{}
	for id, o := range u {
		if o.Coin >= 0 {
			out[id] = o
		}
	}
	return out
}

func sortVerdicts(vs []Verdict) {
	key := func(v Verdict) string {
		b, _ := json.Marshal(v.Tx)
		return string(b)
	}
	sort.Slice(vs, func(i, j int) bool { return key(vs[i]) < key(vs[j]) })
}

// recode moves a decoded TLA+ value into a typed struct through JSON.
func recode(v Value, into any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

func ledgerOf(state map[string]Value) (Ledger, error) {
	var raw struct {
		UTxO       map[string]Output `json:"utxo"`
		Slot       int               `json:"slot"`
		Registered bool              `json:"registered"`
		Reward     int               `json:"reward"`
	}
	if err := recode(state, &raw); err != nil {
		return Ledger{}, fmt.Errorf("decode ledger: %w", err)
	}
	if raw.UTxO == nil {
		return Ledger{}, errors.New("state has no utxo")
	}
	return Ledger{UTxO: live(raw.UTxO), Slot: raw.Slot, Registered: raw.Registered, Reward: raw.Reward}, nil
}

// lastOf reads the history variable: empty before the first transaction.
func lastOf(state map[string]Value) (Verdict, bool, error) {
	var last []struct {
		Tx      rawTx `json:"tx"`
		Accepts bool  `json:"accepts"`
	}
	if err := recode(state["last"], &last); err != nil {
		return Verdict{}, false, fmt.Errorf("decode last: %w", err)
	}
	if len(last) == 0 {
		return Verdict{}, false, nil
	}
	after, err := ledgerOf(state)
	if err != nil {
		return Verdict{}, false, err
	}
	return Verdict{Tx: last[0].Tx.abstract(), Accepts: last[0].Accepts, After: &after}, true, nil
}

// ExportWitnesses runs one Witnesses.tla Bug configuration of dir under the
// witness wrapper and returns the cells behind the violation.
func ExportWitnesses(ctx context.Context, tools Tools, dir, cfg string, timeout time.Duration) (WitnessExport, error) {
	st, err := witnessesWrapper.runWrapped(ctx, tools, dir, cfg, timeout)
	if err != nil {
		return WitnessExport{}, err
	}
	ex := WitnessExport{
		Seed: st.seed("ledger-witnesses", witnessesWrapper.model, cfg),
		Era:  st.consts["Era"], Mutation: st.consts["Mutation"],
	}
	if st.run.Outcome != Violation || st.witness == nil {
		return ex, nil
	}
	var docs []struct {
		Invariant string    `json:"invariant"`
		Env       WitEnv    `json:"env"`
		Rows      []WitCell `json:"rows"`
	}
	if err := json.Unmarshal(st.witness, &docs); err != nil || len(docs) != 1 {
		return WitnessExport{}, fmt.Errorf("%s: decode witnesses: %v", ex.Name, err)
	}
	ex.Env, ex.Cells = docs[0].Env, docs[0].Rows
	sort.Strings(ex.Env.Wits)
	sort.Slice(ex.Cells, func(i, j int) bool {
		a, b := ex.Cells[i], ex.Cells[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return string(a.Cell) < string(b.Cell)
	})
	return ex, nil
}

// WalkSpec bounds one random walk over Utxo.tla's ledger.
type WalkSpec struct {
	// Era is one of UtxoEras.
	Era string
	// Depth is the number of transactions offered.
	Depth int
	// Seed is TLC's simulation seed: the same seed gives the same walk.
	Seed int64
}

const walkCfg = `SPECIFICATION WWalk
CONSTANTS
    Era = "%s"
    MinFee = 1
    MinUtxo = 1
    KeyDeposit = 1
    CollPct = 150
    MaxColl = 2
    StartUtxo <- LedgerStart
    MaxTxs = %d
    Mutation = "none"
    WalkDepth = %d
INVARIANTS
    Stop
`

// Walk runs TLC in simulation mode over the ledger of dir's Utxo.tla and
// returns one random walk of spec.Depth offered transactions, each with the
// reference's verdict. A ledger that can accept nothing more ends the walk
// early. The walk is a function of the model text and spec.
func Walk(ctx context.Context, tools Tools, dir string, spec WalkSpec, timeout time.Duration) (UtxoExport, error) {
	if !eraName.MatchString(spec.Era) || spec.Depth < 1 || spec.Depth > 64 {
		return UtxoExport{}, fmt.Errorf("bad walk: era %q depth %d", spec.Era, spec.Depth)
	}
	w := wrapper{model: "Utxo.tla", name: "UtxoWalk.tla", text: utxoWalkModule}
	cfg := fmt.Sprintf(walkCfg, spec.Era, spec.Depth, spec.Depth)
	work, cleanup, err := w.stage(tools, dir, map[string][]byte{"walk.cfg": []byte(cfg)})
	if err != nil {
		return UtxoExport{}, err
	}
	defer cleanup()
	run, err := runTLC(ctx, tools, work, w.name, "walk.cfg", timeout,
		"-simulate", "num=1000000", "-depth", strconv.Itoa(spec.Depth+1),
		"-seed", strconv.FormatInt(spec.Seed, 10))
	if err != nil {
		return UtxoExport{}, err
	}
	st := staged{run: run}
	ex := UtxoExport{Seed: st.seed("ledger-utxo", w.model, "walk"), Era: spec.Era, Mutation: "none"}
	ex.Name = fmt.Sprintf("ledger-utxo/walk-%s-d%d-s%d", strings.ToLower(spec.Era), spec.Depth, spec.Seed)
	if run.Outcome != Violation {
		return UtxoExport{}, fmt.Errorf("walk %s: tlc %s:\n%s", ex.Name, run.Outcome, excerpt(run.Output))
	}
	return runExport(ex)
}

// BugCfgs lists the *Bug.cfg files of specs/<family> in repo, in name order.
func BugCfgs(repo, family string) (dir string, cfgs []string, err error) {
	jobs, err := listJobs(repo, []string{family})
	if err != nil {
		return "", nil, err
	}
	for _, j := range jobs {
		dir = j.dir
		cfgs = append(cfgs, j.cfg)
	}
	if dir == "" {
		dir = filepath.Join(repo, "specs", family)
	}
	return dir, cfgs, nil
}
