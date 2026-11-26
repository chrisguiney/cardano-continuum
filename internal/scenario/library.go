package scenario

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// maxFile bounds one scenario file.
const maxFile = 4 << 20

// idForm is what a scenario ID may be: slash-separated names, so an ID is
// also the file's path below the library directory.
var idForm = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*(/[A-Za-z0-9][A-Za-z0-9_.-]*)*$`)

// Decode reads one scenario. Unknown fields and other format versions are
// errors: a scenario is never partly understood.
func Decode(r io.Reader) (Scenario, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxFile+1))
	if err != nil {
		return Scenario{}, fmt.Errorf("read scenario: %w", err)
	}
	if len(raw) > maxFile {
		return Scenario{}, fmt.Errorf("scenario larger than %d MiB", maxFile>>20)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Scenario
	if err := dec.Decode(&s); err != nil {
		return Scenario{}, fmt.Errorf("decode scenario: %w", err)
	}
	return s, s.check()
}

func (s Scenario) check() error {
	if s.Version != FormatVersion {
		return fmt.Errorf("scenario %q: format version %d, this build reads %d", s.ID, s.Version, FormatVersion)
	}
	if !idForm.MatchString(s.ID) || strings.Contains(s.ID, "..") {
		return fmt.Errorf("bad scenario id %q", s.ID)
	}
	set := 0
	for _, has := range []bool{s.Utxo != nil, s.Witness != nil, s.Codec != nil} {
		if has {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("scenario %q: want exactly one case, have %d", s.ID, set)
	}
	return nil
}

// Save writes s below dir at <id>.json.
func Save(dir string, s Scenario) error {
	if err := s.check(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", s.ID, err)
	}
	path := filepath.Join(dir, filepath.FromSlash(s.ID)+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Load reads the scenario file at path, or every *.json below it when it is
// a directory, in path order.
func Load(path string) ([]Scenario, error) {
	var files []string
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// *.seed.json files are the harvester's raw seeds, which share the
		// library directory by default.
		if !d.IsDir() && strings.HasSuffix(p, ".json") && !strings.HasSuffix(p, ".seed.json") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load scenarios: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no scenario files at %s", path)
	}
	sort.Strings(files)
	out := make([]Scenario, 0, len(files))
	for _, f := range files {
		s, err := loadFile(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func loadFile(path string) (Scenario, error) {
	f, err := os.Open(path)
	if err != nil {
		return Scenario{}, err
	}
	defer func() { _ = f.Close() }()
	return Decode(f)
}

// Find resolves what `scenario run` was given: a file or directory path, or
// an ID (or ID prefix naming a directory) below lib.
func Find(lib, arg string) ([]Scenario, error) {
	if _, err := os.Stat(arg); err == nil {
		return Load(arg)
	}
	if !idForm.MatchString(arg) || strings.Contains(arg, "..") {
		return nil, fmt.Errorf("%q is neither a path nor a scenario id", arg)
	}
	base := filepath.Join(lib, filepath.FromSlash(arg))
	for _, p := range []string{base + ".json", base} {
		if _, err := os.Stat(p); err == nil {
			return Load(p)
		}
	}
	return nil, fmt.Errorf("scenario %q: %w", arg, errors.Join(ErrNotFound, fs.ErrNotExist))
}

// ErrNotFound reports a scenario ID with no file in the library.
var ErrNotFound = errors.New("not in the library")
