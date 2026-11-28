// Command gen writes internal/contract/registry_gen.go and types_gen.go
// from a CIP-57 blueprint: go generate runs it with -in, -out and -types.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
)

func main() {
	in := flag.String("in", "", "the plutus.json to read")
	out := flag.String("out", "", "the registry Go file to write")
	types := flag.String("types", "", "the datum and redeemer types Go file to write")
	src := flag.String("src", "", "the Aiken project whose sources to snapshot")
	srcOut := flag.String("srcout", "", "the Go file to write the source snapshot to")
	flag.Parse()
	if err := run(*in, *out, *types); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}

	if err := runSources(*src, *srcOut); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(in, out, types string) error {
	raw, err := os.ReadFile(in)
	if err != nil {
		return fmt.Errorf("read blueprint: %w", err)
	}

	bp, err := contract.Parse(raw)
	if err != nil {
		return err
	}

	// The header names the blueprint relative to the repository, so the
	// file is the same wherever the checkout lives.
	src := filepath.ToSlash(filepath.Clean(in))
	for strings.HasPrefix(src, "../") {
		src = strings.TrimPrefix(src, "../")
	}

	code, err := contract.Render(bp, src)
	if err != nil {
		return err
	}

	if err := os.WriteFile(out, code, 0o644); err != nil {
		return fmt.Errorf("write registry: %w", err)
	}

	tcode, err := contract.RenderTypes(bp, src)
	if err != nil {
		return err
	}

	if err := os.WriteFile(types, tcode, 0o644); err != nil {
		return fmt.Errorf("write types: %w", err)
	}
	return nil
}

func runSources(src, out string) error {
	files, err := contract.ReadSources(os.DirFS(src))
	if err != nil {
		return err
	}

	code, err := contract.RenderSources(files)
	if err != nil {
		return err
	}

	if err := os.WriteFile(out, code, 0o644); err != nil {
		return fmt.Errorf("write sources: %w", err)
	}
	return nil
}
