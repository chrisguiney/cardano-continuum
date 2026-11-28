package contract

import (
	"fmt"

	"github.com/blinklabs-io/plutigo/syn"
)

// maxNameDepth bounds the lambda nesting rename follows, so a hostile
// program cannot exhaust the stack; plutigo's printer has its own limit.
const maxNameDepth = 4096

// named returns p with each de Bruijn variable replaced by the name of
// the lambda that binds it: the outermost binder is v0, its first inner
// one v1, and so on. The result is the program in the textual syntax the
// UPLC specification and plutigo's parser use, where a variable is a
// name, not an index. Names are by depth, so none shadows another.
func named(p *syn.Program[syn.DeBruijn]) (*syn.Program[syn.Name], error) {
	t, err := rename(p.Term, 0)
	if err != nil {
		return nil, err
	}
	return &syn.Program[syn.Name]{Version: p.Version, Term: t}, nil
}

func nameAt(depth int) syn.Name {
	return syn.Name{Text: fmt.Sprintf("v%d", depth), Unique: syn.Unique(depth)}
}

// rename converts t under depth enclosing lambdas.
func rename(t syn.Term[syn.DeBruijn], depth int) (syn.Term[syn.Name], error) {
	if depth > maxNameDepth {
		return nil, fmt.Errorf("lambdas nest deeper than %d", maxNameDepth)
	}

	switch t := t.(type) {
	case *syn.Var[syn.DeBruijn]:
		bound := depth - t.Name.LookupIndex()
		if bound < 0 || bound >= depth {
			return nil, fmt.Errorf("variable index %d is free at depth %d", t.Name.LookupIndex(), depth)
		}
		return &syn.Var[syn.Name]{Name: nameAt(bound)}, nil
	case *syn.Lambda[syn.DeBruijn]:
		body, err := rename(t.Body, depth+1)
		if err != nil {
			return nil, err
		}
		return &syn.Lambda[syn.Name]{ParameterName: nameAt(depth), Body: body}, nil
	case *syn.Apply[syn.DeBruijn]:
		f, err := rename(t.Function, depth)
		if err != nil {
			return nil, err
		}

		a, err := rename(t.Argument, depth)
		if err != nil {
			return nil, err
		}
		return &syn.Apply[syn.Name]{Function: f, Argument: a}, nil
	case *syn.Delay[syn.DeBruijn]:
		x, err := rename(t.Term, depth)
		if err != nil {
			return nil, err
		}
		return &syn.Delay[syn.Name]{Term: x}, nil
	case *syn.Force[syn.DeBruijn]:
		x, err := rename(t.Term, depth)
		if err != nil {
			return nil, err
		}
		return &syn.Force[syn.Name]{Term: x}, nil
	case *syn.Constr[syn.DeBruijn]:
		fs, err := renameAll(t.Fields, depth)
		if err != nil {
			return nil, err
		}
		return &syn.Constr[syn.Name]{Tag: t.Tag, Fields: fs}, nil
	case *syn.Case[syn.DeBruijn]:
		c, err := rename(t.Constr, depth)
		if err != nil {
			return nil, err
		}

		bs, err := renameAll(t.Branches, depth)
		if err != nil {
			return nil, err
		}
		return &syn.Case[syn.Name]{Constr: c, Branches: bs}, nil
	case *syn.Builtin:
		return t, nil
	case *syn.Constant:
		return t, nil
	case *syn.Error:
		return t, nil
	}
	return nil, fmt.Errorf("unknown term %T", t)
}

func renameAll(ts []syn.Term[syn.DeBruijn], depth int) ([]syn.Term[syn.Name], error) {
	out := make([]syn.Term[syn.Name], len(ts))
	for i, t := range ts {
		x, err := rename(t, depth)
		if err != nil {
			return nil, err
		}
		out[i] = x
	}
	return out, nil
}
