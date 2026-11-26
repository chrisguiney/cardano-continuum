package tla

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Provenance says exactly which model text and checker produced a seed.
type Provenance struct {
	Repo      string `json:"repo"`
	Commit    string `json:"commit"`
	Family    string `json:"family"`
	Spec      string `json:"spec"`
	Cfg       string `json:"cfg"`
	Invariant string `json:"invariant"`
	TLC       string `json:"tlc"`
}

// Seed is the scenario-seed file written for one *Bug.cfg. Only a Violation
// is a finding; Broken, Passed and TimedOut seeds record that the cfg did not
// do what a Bug cfg must, and carry TLC's output excerpt in Detail.
type Seed struct {
	Name   string     `json:"name"`
	Status Outcome    `json:"status"`
	Prov   Provenance `json:"provenance"`
	Trace  *Trace     `json:"trace,omitempty"`
	Detail string     `json:"detail,omitempty"`
}

// Options configure Harvest.
type Options struct {
	// Repo is a git URL or path; Ref pins it. See Fetch.
	Repo, Ref string
	// CacheDir holds the fetched clone; it must be outside any repository.
	CacheDir string
	// Out receives <family>/<cfg>.seed.json files.
	Out string
	// Families limits the harvest to these specs/<family> directories.
	Families []string
	Tools    Tools
	// Timeout bounds each TLC run.
	Timeout time.Duration
	// Workers is the number of TLC runs in flight; 0 means 1.
	Workers int
}

// Summary counts what a harvest found.
type Summary struct {
	Commit     string
	Total      int
	Violations int
	Broken     int
	TimedOut   int
	// Passed counts Bug cfgs that unexpectedly verified.
	Passed int
	// Seeds lists every seed written, by name.
	Seeds []Seed
}

type job struct{ family, dir, spec, cfg string }

// Harvest runs TLC on every *Bug.cfg of the repository and writes one seed
// file per cfg.
func Harvest(ctx context.Context, opts Options) (Summary, error) {
	dir, commit, err := Fetch(ctx, opts.Repo, opts.Ref, opts.CacheDir)
	if err != nil {
		return Summary{}, err
	}
	jobs, err := listJobs(dir, opts.Families)
	if err != nil {
		return Summary{}, err
	}
	seeds := make([]Seed, len(jobs))
	errs := make([]error, len(jobs))
	sem := make(chan struct{}, max(opts.Workers, 1))
	var wg sync.WaitGroup
	for i, j := range jobs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			seeds[i], errs[i] = harvestOne(ctx, opts, commit, j)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	sum := Summary{Commit: commit, Total: len(jobs)}
	for i, s := range seeds {
		if errs[i] != nil {
			return Summary{}, errs[i]
		}
		sum.add(s)
	}
	return sum, nil
}

func (s *Summary) add(seed Seed) {
	switch seed.Status {
	case Violation:
		s.Violations++
	case Broken:
		s.Broken++
	case TimedOut:
		s.TimedOut++
	case Passed:
		s.Passed++
	}
	s.Seeds = append(s.Seeds, seed)
}

func harvestOne(ctx context.Context, opts Options, commit string, j job) (Seed, error) {
	run, err := RunTLC(ctx, opts.Tools, j.dir, j.spec, j.cfg, opts.Timeout)
	if err != nil {
		return Seed{}, err
	}
	seed := Seed{
		Name:   j.family + "/" + strings.TrimSuffix(j.cfg, ".cfg"),
		Status: run.Outcome,
		Prov: Provenance{
			Repo: opts.Repo, Commit: commit, Family: j.family, Spec: j.spec,
			Cfg: j.cfg, Invariant: run.Invariant, TLC: run.Version,
		},
		Trace: run.Trace,
	}
	if run.Outcome != Violation {
		seed.Detail = excerpt(run.Output)
	}
	path := filepath.Join(opts.Out, j.family, strings.TrimSuffix(j.cfg, ".cfg")+".seed.json")
	if err := writeJSON(path, seed); err != nil {
		return Seed{}, err
	}
	return seed, nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// listJobs finds every specs/<family>/*Bug.cfg, in a stable order.
func listJobs(repo string, families []string) ([]job, error) {
	specs := filepath.Join(repo, "specs")
	if len(families) == 0 {
		ents, err := os.ReadDir(specs)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", specs, err)
		}
		for _, e := range ents {
			if e.IsDir() {
				families = append(families, e.Name())
			}
		}
	}
	sort.Strings(families)
	var jobs []job
	for _, fam := range families {
		if fam != filepath.Base(fam) || fam == "." || fam == ".." {
			return nil, fmt.Errorf("bad family name %q", fam)
		}
		dir := filepath.Join(specs, fam)
		cfgs, err := filepath.Glob(filepath.Join(dir, "*Bug.cfg"))
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("family %s: %w", fam, err)
		}
		sort.Strings(cfgs)
		for _, c := range cfgs {
			cfg := filepath.Base(c)
			spec, err := specFor(dir, cfg)
			if err != nil {
				return nil, fmt.Errorf("family %s: %w", fam, err)
			}
			jobs = append(jobs, job{fam, dir, spec, cfg})
		}
	}
	return jobs, nil
}

var specLine = regexp.MustCompile(`^\\\*\s*SPEC:\s*(\S+)`)

// specFor names the module a cfg checks: its "\* SPEC: Name.tla" header line,
// else the module whose name is the longest prefix of the cfg's name. This is
// the rule the repositories' own run scripts use.
func specFor(dir, cfg string) (string, error) {
	f, err := os.Open(filepath.Join(dir, cfg))
	if err != nil {
		return "", fmt.Errorf("open %s: %w", cfg, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := specLine.FindStringSubmatch(sc.Text()); m != nil {
			return m[1], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", cfg, err)
	}
	mods, err := filepath.Glob(filepath.Join(dir, "*.tla"))
	if err != nil {
		return "", err
	}
	name, best := "", 0
	for _, m := range mods {
		base := strings.TrimSuffix(filepath.Base(m), ".tla")
		if strings.HasPrefix(cfg, base) && len(base) > best {
			name, best = base+".tla", len(base)
		}
	}
	if name == "" {
		return "", fmt.Errorf("no module matches %s", cfg)
	}
	return name, nil
}
