package scenario

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/tlaflags"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	iscenario "github.com/chrisguiney/cardano-continuum/internal/scenario"
	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

var errScenarioUsage = errors.New("usage: continuum scenario run|harvest|vectors [flags]")

// Run dispatches `continuum scenario run|harvest|vectors`.
func Run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errScenarioUsage
	}
	switch args[0] {
	case "run":
		return cmdScenarioRun(ctx, args[1:], out)
	case "harvest":
		return cmdScenarioHarvest(ctx, args[1:], out)
	case "vectors":
		return cmdScenarioVectors(ctx, args[1:], out)
	}
	return fmt.Errorf("%w: unknown subcommand %q", errScenarioUsage, args[0])
}

// runFlags are the flags of the subcommands that execute scenarios.
type runFlags struct {
	target  string
	seed    uint64
	verbose bool
}

func (f *runFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.target, "target", "fake", "where to run: fake (the in-memory chain) or live (a devnet)")
	fs.Uint64Var(&f.seed, "seed", 1, "seed the scenario's keys derive from")
	fs.BoolVar(&f.verbose, "v", false, "print one line per scenario before the summary")
}

// errNoLive reports that the devnet backend is not wired into this command.
var errNoLive = errors.New("scenario: --target live needs a devnet backend, which this build does not have; use --target fake")

func (f *runFlags) backend() (iscenario.Backend, rng.Seed, error) {
	var seed rng.Seed
	for i := range 8 {
		seed[i] = byte(f.seed >> (8 * i))
	}
	switch f.target {
	case "fake":
		return iscenario.FakeBackend{}, seed, nil
	case "live":
		return nil, seed, errNoLive
	}
	return nil, seed, fmt.Errorf("scenario: unknown target %q", f.target)
}

// execute runs every scenario and prints the summary. A disagreement is a
// result, not a failure of the command.
func (f *runFlags) execute(ctx context.Context, ss []iscenario.Scenario, out io.Writer) error {
	b, seed, err := f.backend()
	if err != nil {
		return err
	}
	results := make([]iscenario.Result, 0, len(ss))
	for _, s := range ss {
		res, err := iscenario.Run(ctx, s, b, seed)
		if err != nil {
			return err
		}
		results = append(results, res)
		if f.verbose {
			_, _ = fmt.Fprintf(out, "%s\t%s\t%d/%d steps\t%s\n", res.Status, res.ID, res.Ran, res.Steps, res.Reason)
		}
	}
	return iscenario.Summarize(results).Write(out)
}

func cmdScenarioRun(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scenario run", flag.ContinueOnError)
	var rf runFlags
	rf.register(fs)
	lib := fs.String("lib", "scenarios", "scenario library directory that ids are looked up in")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("scenario run: name a scenario id, file or directory")
	}
	var ss []iscenario.Scenario
	for _, arg := range fs.Args() {
		found, err := iscenario.Find(*lib, arg)
		if err != nil {
			return fmt.Errorf("scenario run: %w", err)
		}
		ss = append(ss, found...)
	}
	return rf.execute(ctx, ss, out)
}

func cmdScenarioVectors(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scenario vectors", flag.ContinueOnError)
	var rf runFlags
	rf.register(fs)
	n := fs.Int("sample", 200, "rows to sample from each vector file")
	repo := fs.String("repo", "", "model repository the vectors came from, for provenance")
	ref := fs.String("ref", "", "model commit the vectors came from, for provenance")
	save := fs.String("save", "", "also write the sampled scenarios below this directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("scenario vectors: name one or more <Era>.vectors.jsonl files")
	}
	_, seed, err := rf.backend()
	if err != nil {
		return err
	}
	var ss []iscenario.Scenario
	for _, path := range fs.Args() {
		sampled, err := sampleFile(path, *n, seed, *repo, *ref)
		if err != nil {
			return fmt.Errorf("scenario vectors: %w", err)
		}
		ss = append(ss, sampled...)
	}
	if err := saveAll(*save, ss); err != nil {
		return err
	}
	return rf.execute(ctx, ss, out)
}

func sampleFile(path string, n int, seed rng.Seed, repo, ref string) ([]iscenario.Scenario, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	rnd := seed.Derive("scenario", "vectors", filepath.Base(path)).Rand()
	return iscenario.SampleVectors(f, n, rnd, repo, ref)
}

func saveAll(dir string, ss []iscenario.Scenario) error {
	if dir == "" {
		return nil
	}
	for _, s := range ss {
		if err := iscenario.Save(dir, s); err != nil {
			return err
		}
	}
	return nil
}

// harvestFlags configure `scenario harvest`.
type harvestFlags struct {
	tlaflags.Flags
	rows, walks, depth int
	walkSeed           int64
	era                string
}

func cmdScenarioHarvest(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scenario harvest", flag.ContinueOnError)
	var f harvestFlags
	f.Register(fs)
	fs.IntVar(&f.rows, "rows", 4, "scenarios kept per table violation")
	fs.IntVar(&f.walks, "walks", 0, "Mode B: random walks over Utxo.tla's ledger")
	fs.IntVar(&f.depth, "depth", 6, "transactions offered per walk")
	fs.Int64Var(&f.walkSeed, "walk-seed", 1, "seed of the first walk; walk i uses seed+i")
	fs.StringVar(&f.era, "era", "Mary", "era the walks run in; from Alonzo most steps need a Plutus script, which is not attached")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tools, cleanup, err := f.Resolve("scenario harvest")
	if err != nil {
		return err
	}
	defer cleanup()
	dir, commit, err := tla.Fetch(ctx, f.Repo, f.Ref, f.Cache)
	if err != nil {
		return fmt.Errorf("scenario harvest: %w", err)
	}
	h := harvester{f: f, tools: tools, dir: dir, commit: commit}
	ss, err := h.all(ctx)
	if err != nil {
		return fmt.Errorf("scenario harvest: %w", err)
	}
	if err := saveAll(f.Out, ss); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "commit %s: %d scenarios written to %s\n", commit, len(ss), f.Out)
	return routing(dir, out)
}

// harvester turns one model repository into scenarios.
type harvester struct {
	f      harvestFlags
	tools  tla.Tools
	dir    string
	commit string
}

func (h harvester) all(ctx context.Context) ([]iscenario.Scenario, error) {
	var out []iscenario.Scenario
	for _, part := range []func(context.Context) ([]iscenario.Scenario, error){h.utxo, h.witnesses, h.codec, h.walk} {
		ss, err := part(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, ss...)
	}
	return out, nil
}

// has reports whether the repository models a family.
func (h harvester) has(family string) bool {
	_, err := os.Stat(filepath.Join(h.dir, "specs", family))
	return err == nil
}

func (h harvester) utxo(ctx context.Context) ([]iscenario.Scenario, error) {
	if !h.has(iscenario.FamilyUtxo) {
		return nil, nil
	}
	dir, cfgs, err := tla.BugCfgs(h.dir, iscenario.FamilyUtxo)
	if err != nil {
		return nil, err
	}
	var out []iscenario.Scenario
	for _, cfg := range cfgs {
		ex, err := tla.ExportUtxo(ctx, h.tools, dir, cfg, h.f.Timeout)
		if err != nil {
			return nil, err
		}
		if ex.Status != tla.Violation {
			return nil, fmt.Errorf("%s: tlc %s: %s", ex.Name, ex.Status, ex.Detail)
		}
		out = append(out, iscenario.FromUtxo(ex, iscenario.SourceTrace, h.f.Repo, h.commit, h.f.rows)...)
	}
	return out, nil
}

func (h harvester) witnesses(ctx context.Context) ([]iscenario.Scenario, error) {
	if !h.has(iscenario.FamilyWitnesses) {
		return nil, nil
	}
	dir, cfgs, err := tla.BugCfgs(h.dir, iscenario.FamilyWitnesses)
	if err != nil {
		return nil, err
	}
	var out []iscenario.Scenario
	for _, cfg := range cfgs {
		ex, err := tla.ExportWitnesses(ctx, h.tools, dir, cfg, h.f.Timeout)
		if err != nil {
			return nil, err
		}
		if ex.Status != tla.Violation {
			return nil, fmt.Errorf("%s: tlc %s: %s", ex.Name, ex.Status, ex.Detail)
		}
		out = append(out, iscenario.FromWitnesses(ex, h.f.Repo, h.commit, h.f.rows)...)
	}
	return out, nil
}

// codecFamilies are the model directories whose Bug cfgs are codec tables.
var codecFamilies = []string{"cbor-codec", "ledger-codec", "txsize-framing"}

func (h harvester) codec(ctx context.Context) ([]iscenario.Scenario, error) {
	var fams []string
	for _, fam := range codecFamilies {
		if h.has(fam) {
			fams = append(fams, fam)
		}
	}
	if len(fams) == 0 {
		return nil, nil
	}
	seeds, err := os.MkdirTemp(h.tools.Tmp, "seeds-")
	if err != nil {
		return nil, err
	}
	sum, err := tla.Harvest(ctx, tla.Options{
		Repo: h.dir, Out: seeds, Families: fams, Tools: h.tools, Timeout: h.f.Timeout, Workers: h.f.Workers,
	})
	if err != nil {
		return nil, err
	}
	var out []iscenario.Scenario
	for _, seed := range sum.Seeds {
		if seed.Status != tla.Violation {
			return nil, fmt.Errorf("%s: tlc %s: %s", seed.Name, seed.Status, seed.Detail)
		}
		s, err := iscenario.FromCodecSeed(seed, h.f.Repo, h.commit)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if h.has("cbor-codec") {
		out = append(out, iscenario.CodecTable(h.f.Repo, h.commit)...)
	}
	return out, nil
}

func (h harvester) walk(ctx context.Context) ([]iscenario.Scenario, error) {
	if h.f.walks == 0 || !h.has(iscenario.FamilyUtxo) {
		return nil, nil
	}
	dir := filepath.Join(h.dir, "specs", iscenario.FamilyUtxo)
	var out []iscenario.Scenario
	for i := range h.f.walks {
		spec := tla.WalkSpec{Era: h.f.era, Depth: h.f.depth, Seed: h.f.walkSeed + int64(i)}
		ex, err := tla.Walk(ctx, h.tools, dir, spec, h.f.Timeout)
		if err != nil {
			return nil, err
		}
		out = append(out, iscenario.FromUtxo(ex, iscenario.SourceWalk, h.f.Repo, h.commit, 0)...)
	}
	return out, nil
}

// routing prints, for every family of the repository, how many Bug cfgs it
// has and which adversary they belong to.
func routing(dir string, out io.Writer) error {
	ents, err := os.ReadDir(filepath.Join(dir, "specs"))
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "family\tbug cfgs\towner")
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		_, cfgs, err := tla.BugCfgs(dir, name)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\n", name, len(cfgs), iscenario.OwnerOf(name))
	}
	return tw.Flush()
}
