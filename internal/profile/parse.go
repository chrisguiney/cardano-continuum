package profile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only profile file version this package reads.
const SchemaVersion = 1

// maxInput bounds how much Parse reads; profile files are small hand-written
// documents, so anything larger is a mistake or an attack.
const maxInput = 1 << 20

// weightTolerance is how far intent weights may sum from 1 before rejection.
const weightTolerance = 1e-6

var identRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type rawFile struct {
	Version  int          `yaml:"version"`
	Profiles []rawProfile `yaml:"profiles"`
}

type rawProfile struct {
	Name      string             `yaml:"name"`
	Kind      string             `yaml:"kind"`
	Agenda    string             `yaml:"agenda"`
	Traits    rawTraits          `yaml:"traits"`
	Budget    int64              `yaml:"budget,omitempty"`
	Rates     rawRates           `yaml:"rates,omitempty"`
	Weights   map[string]float64 `yaml:"weights,omitempty"`
	Hazards   rawHazards         `yaml:"hazards,omitempty"`
	Adversary *rawAdversary      `yaml:"adversary,omitempty"`
	Amount    *rawAmount         `yaml:"amount,omitempty"`
	Weight    float64            `yaml:"weight,omitempty"`
}

type rawAmount struct {
	Body      string  `yaml:"body"`
	Tail      string  `yaml:"tail,omitempty"`
	TailShare float64 `yaml:"tail_share,omitempty"`
}

type rawTraits struct {
	Competence   string `yaml:"competence"`
	Patience     string `yaml:"patience"`
	LearningRate string `yaml:"learning_rate"`
	Care         string `yaml:"care"`
}

type rawRates struct {
	Base    map[string]float64 `yaml:"base,omitempty"`
	Diurnal float64            `yaml:"diurnal,omitempty"`
	Hawkes  rawHawkes          `yaml:"hawkes,omitempty"`
	Tail    float64            `yaml:"activity_tail,omitempty"`
}

type rawHawkes struct {
	Alpha float64 `yaml:"alpha,omitempty"`
	Beta  float64 `yaml:"beta,omitempty"`
}

type rawHazards struct {
	Birth    float64 `yaml:"birth,omitempty"`
	Dormancy string  `yaml:"dormancy,omitempty"`
	Abandon  float64 `yaml:"abandon,omitempty"`
	Onset    float64 `yaml:"dormancy_onset,omitempty"`
}

type rawAdversary struct {
	Attacks       []string `yaml:"attacks"`
	CostCeiling   int64    `yaml:"cost_ceiling"`
	SuccessMetric string   `yaml:"success_metric,omitempty"`
}

// Parse reads a profile file from r. See ParseNamed.
func Parse(r io.Reader) ([]Profile, error) { return ParseNamed("", r) }

// ParseNamed reads and validates a profile file, reading at most 1 MiB. Unknown
// fields are errors. Errors name file (when non-empty), profile and field.
func ParseNamed(file string, r io.Reader) ([]Profile, error) {
	ps, err := parse(r)
	if err != nil && file != "" {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return ps, err
}

func parse(r io.Reader) ([]Profile, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxInput+1))
	if err != nil {
		return nil, fmt.Errorf("read profiles: %w", err)
	}
	if len(data) > maxInput {
		return nil, fmt.Errorf("profile file exceeds %d bytes", maxInput)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f rawFile
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("profile file is empty")
		}
		return nil, fmt.Errorf("parse profiles: %w", err)
	}
	if err := dec.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return nil, errors.New("profile file holds more than one YAML document")
	}
	if f.Version != SchemaVersion {
		return nil, fmt.Errorf("version: got %d, want %d", f.Version, SchemaVersion)
	}
	if len(f.Profiles) == 0 {
		return nil, errors.New("profiles: none defined")
	}
	out := make([]Profile, 0, len(f.Profiles))
	for i, rp := range f.Profiles {
		p, err := rp.validate()
		if err != nil {
			label := fmt.Sprintf("profile[%d]", i)
			if rp.Name != "" {
				label = fmt.Sprintf("profile %q", rp.Name)
			}
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func fieldErr(field, format string, a ...any) error {
	return fmt.Errorf("%s: %s", field, fmt.Sprintf(format, a...))
}

func finiteNonNeg(field string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return fieldErr(field, "must be finite and >= 0, got %s", ff(v))
	}
	return nil
}

func sortedKeys(m map[string]float64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (rp rawProfile) validate() (Profile, error) {
	var p Profile
	if !identRE.MatchString(rp.Name) {
		return p, fieldErr("name", "%q must match %s", rp.Name, identRE)
	}
	p.Name = rp.Name
	p.Kind = Kind(rp.Kind)
	if !kinds[p.Kind] {
		return p, fieldErr("kind", "unknown kind %q", rp.Kind)
	}
	p.Agenda = AgendaName(rp.Agenda)
	if !agendas[p.Agenda] {
		return p, fieldErr("agenda", "unknown agenda %q", rp.Agenda)
	}
	if err := rp.Traits.validate(&p.Traits); err != nil {
		return p, err
	}
	if rp.Budget < 0 {
		return p, fieldErr("budget", "must be >= 0, got %d", rp.Budget)
	}
	p.Budget = Lovelace(rp.Budget)
	if err := rp.Rates.validate(&p.Rates); err != nil {
		return p, err
	}
	if len(rp.Weights) > 0 {
		var sum float64
		p.Weights = make(map[IntentKind]float64, len(rp.Weights))
		for _, k := range sortedKeys(rp.Weights) {
			w := rp.Weights[k]
			if !intents[IntentKind(k)] {
				return p, fieldErr("weights."+k, "unknown intent")
			}
			if err := finiteNonNeg("weights."+k, w); err != nil {
				return p, err
			}
			p.Weights[IntentKind(k)] = w
			sum += w
		}
		if math.Abs(sum-1) > weightTolerance {
			return p, fieldErr("weights", "must sum to 1, got %s", ff(sum))
		}
	}
	if err := rp.Hazards.validate(&p.Hazards); err != nil {
		return p, err
	}
	if err := finiteNonNeg("weight", rp.Weight); err != nil {
		return p, err
	}
	p.Weight = rp.Weight
	if rp.Amount != nil {
		a, err := rp.Amount.validate()
		if err != nil {
			return p, err
		}
		p.Amount = a
	}
	if rp.Adversary != nil {
		a, err := rp.Adversary.validate()
		if err != nil {
			return p, err
		}
		p.Adversary = a
	}
	return p, nil
}

func (rt rawTraits) validate(t *Traits) error {
	d, err := parseDist(rt.Competence)
	if err != nil {
		return fieldErr("traits.competence", "%v", err)
	}
	b, ok := d.(Beta)
	if !ok {
		return fieldErr("traits.competence", "must be a beta distribution, got %s", d)
	}
	t.Competence = b
	for _, f := range []struct {
		name, text string
		dst        *Dist
	}{
		{"traits.patience", rt.Patience, &t.Patience},
		{"traits.learning_rate", rt.LearningRate, &t.LearningRate},
		{"traits.care", rt.Care, &t.Care},
	} {
		d, err := parseDist(f.text)
		if err != nil {
			return fieldErr(f.name, "%v", err)
		}
		*f.dst = d
	}
	return nil
}

func (rr rawRates) validate(r *Rates) error {
	if len(rr.Base) > 0 {
		r.Base = make(map[IntentKind]float64, len(rr.Base))
		for _, k := range sortedKeys(rr.Base) {
			if !intents[IntentKind(k)] {
				return fieldErr("rates.base."+k, "unknown intent")
			}
			if err := finiteNonNeg("rates.base."+k, rr.Base[k]); err != nil {
				return err
			}
			r.Base[IntentKind(k)] = rr.Base[k]
		}
	}
	if err := finiteNonNeg("rates.diurnal", rr.Diurnal); err != nil {
		return err
	}
	if rr.Diurnal > 1 {
		return fieldErr("rates.diurnal", "must be <= 1, got %s", ff(rr.Diurnal))
	}
	r.Diurnal = rr.Diurnal
	h := rr.Hawkes
	if h != (rawHawkes{}) {
		if err := finiteNonNeg("rates.hawkes", h.Alpha); err != nil {
			return err
		}
		if err := finiteNonNeg("rates.hawkes", h.Beta); err != nil {
			return err
		}
		if !(h.Alpha < h.Beta) {
			return fieldErr("rates.hawkes", "alpha must be < beta for a stationary process, got alpha=%s beta=%s", ff(h.Alpha), ff(h.Beta))
		}
	}
	r.Hawkes = Hawkes(h)
	if err := finiteNonNeg("rates.activity_tail", rr.Tail); err != nil {
		return err
	}
	// A tail index at or below 1 gives the multiplier an infinite mean, so
	// one actor could dominate the whole run's activity.
	if rr.Tail != 0 && rr.Tail <= 1 {
		return fieldErr("rates.activity_tail", "must be > 1, got %s", ff(rr.Tail))
	}
	r.Tail = rr.Tail
	return nil
}

func (rh rawHazards) validate(h *Hazards) error {
	if err := finiteNonNeg("hazards.birth", rh.Birth); err != nil {
		return err
	}
	if err := finiteNonNeg("hazards.abandon", rh.Abandon); err != nil {
		return err
	}
	if err := finiteNonNeg("hazards.dormancy_onset", rh.Onset); err != nil {
		return err
	}
	h.Birth, h.Abandon, h.Onset = rh.Birth, rh.Abandon, rh.Onset
	if rh.Dormancy != "" {
		d, err := parseDist(rh.Dormancy)
		if err != nil {
			return fieldErr("hazards.dormancy", "%v", err)
		}
		h.Dormancy = d
	}
	return nil
}

func (ra rawAdversary) validate() (*AdversarySpec, error) {
	if len(ra.Attacks) == 0 {
		return nil, fieldErr("adversary.attacks", "at least one attack is required")
	}
	for _, a := range ra.Attacks {
		if !identRE.MatchString(a) {
			return nil, fieldErr("adversary.attacks", "%q must match %s", a, identRE)
		}
	}
	if ra.CostCeiling <= 0 {
		return nil, fieldErr("adversary.cost_ceiling", "required and must be > 0, got %d", ra.CostCeiling)
	}
	if ra.SuccessMetric != "" && !identRE.MatchString(ra.SuccessMetric) {
		return nil, fieldErr("adversary.success_metric", "%q must match %s", ra.SuccessMetric, identRE)
	}
	return &AdversarySpec{
		Attacks:       append([]string(nil), ra.Attacks...),
		CostCeiling:   Lovelace(ra.CostCeiling),
		SuccessMetric: ra.SuccessMetric,
	}, nil
}

// Marshal renders ps in the file format Parse reads. Parse(Marshal(ps)) yields
// a value equal to ps.
func Marshal(ps []Profile) ([]byte, error) {
	f := rawFile{Version: SchemaVersion}
	for _, p := range ps {
		rp := rawProfile{
			Name: p.Name, Kind: string(p.Kind), Agenda: string(p.Agenda),
			Traits: rawTraits{
				Competence: p.Traits.Competence.String(), Patience: p.Traits.Patience.String(),
				LearningRate: p.Traits.LearningRate.String(), Care: p.Traits.Care.String(),
			},
			Budget: int64(p.Budget),
			Rates: rawRates{
				Base: toRaw(p.Rates.Base), Diurnal: p.Rates.Diurnal,
				Hawkes: rawHawkes{Alpha: p.Rates.Hawkes.Alpha, Beta: p.Rates.Hawkes.Beta},
				Tail:   p.Rates.Tail,
			},
			Weights: toRaw(p.Weights),
			Hazards: rawHazards{Birth: p.Hazards.Birth, Abandon: p.Hazards.Abandon, Onset: p.Hazards.Onset},
			Weight:  p.Weight,
		}
		if a := p.Amount; a != nil {
			rp.Amount = &rawAmount{Body: a.Body.String(), TailShare: a.TailShare}
			if a.Tail != nil {
				rp.Amount.Tail = a.Tail.String()
			}
		}
		if p.Hazards.Dormancy != nil {
			rp.Hazards.Dormancy = p.Hazards.Dormancy.String()
		}
		if a := p.Adversary; a != nil {
			rp.Adversary = &rawAdversary{Attacks: a.Attacks, CostCeiling: int64(a.CostCeiling), SuccessMetric: a.SuccessMetric}
		}
		f.Profiles = append(f.Profiles, rp)
	}
	return yaml.Marshal(f)
}

func toRaw(m map[IntentKind]float64) map[string]float64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}
