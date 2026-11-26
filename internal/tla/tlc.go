package tla

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxOutput bounds the TLC output kept in memory.
const maxOutput = 4 << 20

// Tools locates the model checker.
type Tools struct {
	// TLC is the models/bin/tlc wrapper script.
	TLC string
	// Jar is the tla2tools jar; it must be one whose TLC accepts -dumpTrace.
	Jar string
	// Tmp is a private directory for TLC's metadata and Java's temp dir.
	// Concurrent TLC runs race on a shared Java temp dir, so every run gets
	// its own directory below it.
	Tmp string
	// Workers is TLC's -workers value; 0 means 1, which keeps counterexamples
	// reproducible.
	Workers int
}

// Run is the result of one TLC invocation.
type Run struct {
	Outcome   Outcome
	Invariant string
	Version   string
	// Trace is the counterexample of a Violation.
	Trace *Trace
	// Output is TLC's combined stdout and stderr, truncated.
	Output string
}

// RunTLC checks spec under cfg in dir, bounded by timeout, and dumps a JSON
// counterexample if there is one. Only a failure to run TLC at all is an
// error; a parse or evaluation failure in the model is reported as Broken.
func RunTLC(ctx context.Context, tools Tools, dir, spec, cfg string, timeout time.Duration) (Run, error) {
	return runTLC(ctx, tools, dir, spec, cfg, timeout)
}

// runTLC is RunTLC with extra TLC options (the simulation flags) placed
// before -config.
func runTLC(ctx context.Context, tools Tools, dir, spec, cfg string, timeout time.Duration, extra ...string) (Run, error) {
	if err := os.MkdirAll(tools.Tmp, 0o755); err != nil {
		return Run{}, fmt.Errorf("create tmp %s: %w", tools.Tmp, err)
	}
	work, err := os.MkdirTemp(tools.Tmp, "tlc-")
	if err != nil {
		return Run{}, fmt.Errorf("create run dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()
	dump := filepath.Join(work, "trace.json")
	workers := max(tools.Workers, 1)

	args := []string{
		"-noGenerateSpecTE",
		"-workers", strconv.Itoa(workers),
		"-metadir", filepath.Join(work, "states"),
		"-dumpTrace", "json", dump,
	}
	args = append(args, extra...)
	out, exit, timedOut, err := runCmd(ctx, timeout, dir, tools, work, append(args, "-config", cfg, spec))
	if err != nil {
		return Run{}, fmt.Errorf("run tlc %s %s: %w", spec, cfg, err)
	}
	outcome, inv := Classify(out, exit, timedOut)
	run := Run{Outcome: outcome, Invariant: inv, Version: TLCVersion(out), Output: out}
	if outcome != Violation {
		return run, nil
	}
	f, err := os.Open(dump)
	if err == nil {
		defer func() { _ = f.Close() }()
		var tr Trace
		if tr, err = ParseTrace(f); err == nil {
			run.Trace = &tr
			return run, nil
		}
	}
	run.Outcome = Broken
	run.Output = fmt.Sprintf("violation of %s but no usable trace: %v\n%s", inv, err, out)
	return run, nil
}

// runCmd runs the TLC wrapper with an argument vector and its own Java temp
// dir. exit is -1 when the process did not exit on its own.
func runCmd(ctx context.Context, timeout time.Duration, dir string, tools Tools, work string, args []string) (out string, exit int, timedOut bool, err error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	javaTmp := filepath.Join(work, "java")
	if err := os.MkdirAll(javaTmp, 0o755); err != nil {
		return "", 0, false, fmt.Errorf("create java tmp: %w", err)
	}
	cmd := exec.CommandContext(tctx, tools.TLC, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"JAVA_TOOL_OPTIONS=-Djava.io.tmpdir="+javaTmp,
		"TLA2TOOLS_JAR="+tools.Jar,
	)
	cmd.WaitDelay = 5 * time.Second
	buf := &capped{limit: maxOutput}
	cmd.Stdout, cmd.Stderr = buf, buf
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return "", 0, false, ctx.Err()
	}
	if errors.Is(tctx.Err(), context.DeadlineExceeded) {
		return buf.String(), -1, true, nil
	}
	var ee *exec.ExitError
	switch {
	case runErr == nil:
		return buf.String(), 0, false, nil
	case errors.As(runErr, &ee):
		return buf.String(), ee.ExitCode(), false, nil
	}
	return "", 0, false, runErr
}

// capped keeps the first limit bytes written and discards the rest.
type capped struct {
	buf   bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (c *capped) String() string { return c.buf.String() }

// excerpt picks the lines of TLC output worth keeping in a seed file when
// there is no counterexample: the error lines, else the last few lines.
func excerpt(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var keep []string
	for _, l := range lines {
		if strings.Contains(l, "rror") || strings.Contains(l, "violat") {
			keep = append(keep, l)
		}
	}
	if len(keep) == 0 {
		keep = lines[max(0, len(lines)-10):]
	}
	s := strings.Join(keep[:min(len(keep), 15)], "\n")
	if len(s) > 2000 {
		s = s[:2000]
	}
	return s
}
