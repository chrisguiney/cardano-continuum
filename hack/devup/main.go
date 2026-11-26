// Command devup is the Go half of hack/up.sh: it checks memory and renders
// the network manifests. It prints to stdout and reads no stdin.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/chrisguiney/cardano-continuum/internal/devup"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/operator"
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
		fmt.Println(operator.CardanoNode{}.Image())
		fmt.Println(operator.Dingo{}.Image())
		return nil
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
