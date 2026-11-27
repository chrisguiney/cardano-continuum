package profiles

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	shipped "github.com/chrisguiney/cardano-continuum/profiles"
)

// indexEntry is one row of profiles/index.json.
type indexEntry struct {
	Profile string `json:"profile"`
	Target  string `json:"target"`
}

// Run handles `continuum profiles`: today only `profiles index`, which writes
// the profile-hash index of the fitted profiles.
func Run(_ context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "index" {
		return errors.New("usage: continuum profiles index [--out FILE]")
	}
	fs := flag.NewFlagSet("profiles index", flag.ContinueOnError)
	file := fs.String("out", "", "write the index to this file instead of stdout")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	idx := map[string]indexEntry{}
	for _, name := range shipped.Fitted {
		raw, err := shipped.Named(name)
		if err != nil {
			return err
		}
		f, err := profile.ParseFile(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("%s.yaml: %w", name, err)
		}
		h, err := engine.FileHash(f)
		if err != nil {
			return err
		}
		idx[hex.EncodeToString(h[:])] = indexEntry{Profile: name, Target: shipped.TargetFile(name)}
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if *file != "" {
		return os.WriteFile(*file, b, 0o644) //nolint:gosec // a shipped, public file
	}
	_, err = out.Write(b)
	return err
}
