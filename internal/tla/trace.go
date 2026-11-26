package tla

import (
	"fmt"
	"io"
)

// Step is one state of a counterexample and the action that reached it.
type Step struct {
	// Num is TLC's 1-based state number.
	Num int
	// Action names the action taken to reach the state; "Initial" for state 1.
	Action string
	// Params names the action's bound parameters, if any.
	Params []string
	// State maps each variable to its value.
	State map[string]Value
}

// Trace is a counterexample: the variables and the sequence of states.
type Trace struct {
	Vars  []string
	Steps []Step
}

// ParseTrace decodes the file TLC writes for -dumpTrace json.
func ParseTrace(r io.Reader) (Trace, error) {
	v, err := DecodeValue(r)
	if err != nil {
		return Trace{}, err
	}
	root, ok := v.(map[string]Value)
	if !ok {
		return Trace{}, fmt.Errorf("trace: top level is %T, want object", v)
	}
	ce, ok := root["counterexample"].(map[string]Value)
	if !ok {
		return Trace{}, fmt.Errorf("trace: no counterexample object")
	}
	states, _ := ce["state"].([]Value)
	if len(states) == 0 {
		return Trace{}, fmt.Errorf("trace: counterexample has no states")
	}
	tr := Trace{Vars: strList(root["vars"])}
	for i, s := range states {
		step, err := parseStep(s)
		if err != nil {
			return Trace{}, fmt.Errorf("trace: state %d: %w", i+1, err)
		}
		tr.Steps = append(tr.Steps, step)
	}
	acts, _ := ce["action"].([]Value)
	tr.Steps[0].Action = "Initial"
	for i := 1; i < len(tr.Steps); i++ {
		if i-1 >= len(acts) {
			return Trace{}, fmt.Errorf("trace: no action for state %d", i+1)
		}
		name, params, err := parseAction(acts[i-1])
		if err != nil {
			return Trace{}, fmt.Errorf("trace: action into state %d: %w", i+1, err)
		}
		tr.Steps[i].Action, tr.Steps[i].Params = name, params
	}
	return tr, nil
}

// parseStep reads [num, {var: value}].
func parseStep(v Value) (Step, error) {
	pair, ok := v.([]Value)
	if !ok || len(pair) != 2 {
		return Step{}, fmt.Errorf("want [num, state]")
	}
	num, ok := pair[0].(int64)
	if !ok {
		return Step{}, fmt.Errorf("state number is %T", pair[0])
	}
	st, ok := pair[1].(map[string]Value)
	if !ok {
		return Step{}, fmt.Errorf("state is %T, want object", pair[1])
	}
	return Step{Num: int(num), State: st}, nil
}

// parseAction reads [from, {name, parameters}, to].
func parseAction(v Value) (string, []string, error) {
	triple, ok := v.([]Value)
	if !ok || len(triple) != 3 {
		return "", nil, fmt.Errorf("want [from, action, to]")
	}
	a, ok := triple[1].(map[string]Value)
	if !ok {
		return "", nil, fmt.Errorf("action is %T, want object", triple[1])
	}
	name, ok := a["name"].(string)
	if !ok {
		return "", nil, fmt.Errorf("action has no name")
	}
	return name, strList(a["parameters"]), nil
}

// strList returns the string elements of v; anything else is skipped.
func strList(v Value) []string {
	arr, _ := v.([]Value)
	var out []string
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
