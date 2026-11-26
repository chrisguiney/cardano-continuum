// Command devup is the Go half of hack/up.sh: it checks memory, renders the
// network manifests and resolves ATTACKS. It prints to stdout and reads no stdin.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/chrisguiney/cardano-continuum/internal/devup"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	noderender "github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "devup:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: devup mem <quantity> <min-gib> | render -pools N -dingo M")
	}
	switch args[0] {
	case "mem":
		return mem(args[1:])
	case "render":
		return render(args[1:])
	case "images":
		fmt.Println(noderender.CardanoNode{}.Image())
		fmt.Println(noderender.Dingo{}.Image())
		return nil
	case "attacks":
		// Line 1 is the engine's --attack value (empty for none); every
		// further line is one continuum-genesis argument.
		if len(args) != 2 {
			return fmt.Errorf("usage: devup attacks <ATTACKS>")
		}
		p, err := devup.Attacks(args[1])
		if err != nil {
			return err
		}
		fmt.Println(p.Engine)
		for _, a := range p.Genesis {
			fmt.Println(a)
		}
		return nil
	case "run":
		return runIdentity(args[1:])
	case "node":
		if len(args) != 2 {
			return fmt.Errorf("usage: devup node <namespace>")
		}
		fmt.Println(devup.EngineNode(args[1]))
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

// mem prints a warning, and still succeeds, when the machine is short.
func mem(args []string) error {
	var min int64
	if len(args) != 2 {
		return fmt.Errorf("usage: devup mem <quantity> <min-gib>")
	}
	if _, err := fmt.Sscan(args[1], &min); err != nil {
		return fmt.Errorf("min-gib %q: %w", args[1], err)
	}
	ok, err := devup.CheckMemory(args[0], min)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Printf("WARNING: node allocatable memory %s is below %d GiB; see docs/single-machine.md\n", args[0], min)
	}
	return nil
}

func render(args []string) error {
	d := genesis.Default()
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	p := devup.Plan{Magic: d.Magic, EpochLength: d.EpochLength, SecurityParam: d.SecurityParam}
	fs.IntVar(&p.Nodes, "pools", d.Pools, "cardano-node pools")
	fs.IntVar(&p.Dingo, "dingo", 1, "dingo pools")
	if err := fs.Parse(args); err != nil {
		return err
	}
	out, err := devup.Render(p)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

// runIdentity prints the engine run identity for the flags and the SEED
// environment variable (a flag would put the seed in the process list),
// then, on a second line, why an engine deployed under -deployed cannot
// resume it, or an empty line when it can.
func runIdentity(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var r devup.RunIdentity
	deployed := fs.String("deployed", "", "the deployed engine's identity annotation")
	fs.StringVar(&r.Image, "image", "", "engine image")
	fs.StringVar(&r.Profiles, "profiles", "", "PROFILES")
	fs.StringVar(&r.Attacks, "attacks", "", "the engine's --attack value")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r.Seed = os.Getenv("SEED")
	fmt.Println(r.String())
	fmt.Println(devup.RunChange(*deployed, r))
	return nil
}
