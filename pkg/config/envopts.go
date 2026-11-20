package config

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/ext"
	"cel.dev/cel-go/parser"
)

// Options converts an Env declaration into cel-go environment options.
func (e *Env) Options() ([]cel.EnvOption, error) {
	var opts []cel.EnvOption
	if e.Container != "" {
		opts = append(opts, cel.Container(e.Container))
	}
	if len(e.Abbreviations) > 0 {
		opts = append(opts, cel.Abbrevs(e.Abbreviations...))
	}
	for _, name := range e.Extensions {
		o, err := extensionOption(name)
		if err != nil {
			return nil, err
		}
		opts = append(opts, o)
	}
	for _, v := range e.Variables {
		t, err := variableType(v)
		if err != nil {
			return nil, err
		}
		opts = append(opts, cel.VariableWithDoc(v.Name, t, v.Doc))
	}
	for _, fn := range e.Functions {
		o, err := functionOption(fn)
		if err != nil {
			return nil, err
		}
		opts = append(opts, o)
	}
	for _, m := range e.Macros {
		mopts, err := macroOptions(&m)
		if err != nil {
			return nil, err
		}
		opts = append(opts, mopts...)
	}
	if e.Types != nil {
		o, err := e.Types.Options()
		if err != nil {
			return nil, err
		}
		opts = append(opts, o...)
	}
	// Track macro expansions only when macros are declared, so environments
	// without macros don't pay the tracking overhead.
	if len(e.Macros) > 0 {
		opts = append(opts, cel.EnableMacroCallTracking())
	}
	return opts, nil
}

// variableType resolves a variable's CEL type from either a type expression or
// a type_name reference.
func variableType(v Variable) (*cel.Type, error) {
	if v.TypeName != "" {
		return cel.ObjectType(strings.TrimPrefix(v.TypeName, ".")), nil
	}
	t, err := parseType(v.Type)
	if err != nil {
		return nil, fmt.Errorf("env.variables[%q]: %w", v.Name, err)
	}
	return t, nil
}

// extensionOption maps an extension library name to its cel-go option.
func extensionOption(name string) (cel.EnvOption, error) {
	switch name {
	case "optional-types":
		return cel.OptionalTypes(), nil
	case "lists":
		return ext.Lists(), nil
	case "two-var-comprehensions":
		return ext.TwoVarComprehensions(), nil
	case "regex":
		return ext.Regex(), nil
	case "strings":
		return ext.Strings(), nil
	case "network":
		return ext.Network(), nil
	case "encoders":
		return ext.Encoders(), nil
	case "math":
		return ext.Math(), nil
	case "bindings":
		return ext.Bindings(), nil
	case "sets":
		return ext.Sets(), nil
	default:
		return nil, fmt.Errorf("env.extensions: unknown extension %q", name)
	}
}

// functionOption builds a cel.Function option from a declarative function.
func functionOption(fn Function) (cel.EnvOption, error) {
	fopts := []cel.FunctionOpt{}
	if fn.Doc != "" {
		fopts = append(fopts, cel.FunctionDocs(fn.Doc))
	}
	for _, ov := range fn.Overloads {
		args := make([]*cel.Type, 0, len(ov.Args))
		for _, a := range ov.Args {
			t, err := parseType(a)
			if err != nil {
				return nil, fmt.Errorf("env.functions[%q] overload %q: %w", fn.Name, ov.ID, err)
			}
			args = append(args, t)
		}
		res, err := parseType(ov.Result)
		if err != nil {
			return nil, fmt.Errorf("env.functions[%q] overload %q result: %w", fn.Name, ov.ID, err)
		}
		ovOpts := []cel.OverloadOpt{}
		if len(ov.Examples) > 0 {
			ovOpts = append(ovOpts, cel.OverloadExamples(ov.Examples...))
		}
		if ov.Member {
			fopts = append(fopts, cel.MemberOverload(ov.ID, args, res, ovOpts...))
		} else {
			fopts = append(fopts, cel.Overload(ov.ID, args, res, ovOpts...))
		}
	}
	return cel.Function(fn.Name, fopts...), nil
}

// maxMacroArity caps the synthetic function overload arities generated for
// vararg macros, whose arity cel-go's fixed-arity function model cannot
// otherwise represent. Arguments are unconstrained, so a bounded set of
// all-dyn overloads lets any observed call type-check.
const maxMacroArity = 10

// macroOptions builds a signature-only macro. A cel.Macro makes the parser
// accept the call (and surfaces it in completion, hover and misspell) without
// rewriting it; the expander rebuilds the call unchanged so it resolves to a
// synthetic function. A cel.Function provides the output types so the call
// type-checks and hover can report a result type.
func macroOptions(m *Macro) ([]cel.EnvOption, error) {
	opts := []cel.EnvOption{cel.Macros(passthroughMacro(m))}

	fopts := []cel.FunctionOpt{}
	if m.Doc != "" {
		fopts = append(fopts, cel.FunctionDocs(m.Doc))
	}
	// Exact input/output type pairs.
	for idx, ov := range m.Overloads {
		args := make([]*cel.Type, 0, len(ov.Args))
		for _, a := range ov.Args {
			t, err := parseType(a)
			if err != nil {
				return nil, fmt.Errorf("env.macros[%q] overload %q arg: %w", m.Name, ov.ID, err)
			}
			args = append(args, t)
		}
		res, err := parseType(ov.Result)
		if err != nil {
			return nil, fmt.Errorf("env.macros[%q] overload %q result: %w", m.Name, ov.ID, err)
		}
		id := ov.ID
		if id == "" {
			id = fmt.Sprintf("%s_overload_%d", m.Name, idx)
		}
		ovOpts := []cel.OverloadOpt{}
		if len(ov.Examples) > 0 {
			ovOpts = append(ovOpts, cel.OverloadExamples(ov.Examples...))
		}
		if m.Receiver {
			fopts = append(fopts, cel.MemberOverload(id, args, res, ovOpts...))
		} else {
			fopts = append(fopts, cel.Overload(id, args, res, ovOpts...))
		}
	}
	// Plain output types -> unconstrained (dyn) arguments over the accepted
	// arities.
	receiverOffset := 0
	if m.Receiver {
		receiverOffset = 1
	}
	for resIdx, res := range m.Results {
		resType, err := parseType(res)
		if err != nil {
			return nil, fmt.Errorf("env.macros[%q] result %q: %w", m.Name, res, err)
		}
		for _, arity := range macroArities(m) {
			n := arity + receiverOffset
			args := make([]*cel.Type, 0, n)
			for range n {
				args = append(args, cel.DynType)
			}
			id := fmt.Sprintf("%s_result_%d_arity_%d", m.Name, resIdx, arity)
			if m.Receiver {
				fopts = append(fopts, cel.MemberOverload(id, args, resType))
			} else {
				fopts = append(fopts, cel.Overload(id, args, resType))
			}
		}
	}
	return append(opts, cel.Function(m.Name, fopts...)), nil
}

// macroArities returns the argument counts (excluding the receiver) that the
// synthetic function must accept so that any passthrough-expanded call
// type-checks.
func macroArities(m *Macro) []int {
	if !m.VarArg {
		return []int{m.FixedArity}
	}
	arities := make([]int, 0, maxMacroArity+1)
	for i := 0; i <= maxMacroArity; i++ {
		arities = append(arities, i)
	}
	return arities
}

// passthroughMacro builds the cel.Macro for parser acceptance. The expander
// rebuilds the call as a plain function call (no rewriting), so the parser
// accepts it, type-check resolves it against the synthetic function, and the
// macro is surfaced in completion, hover and misspell.
func passthroughMacro(m *Macro) cel.Macro {
	noop := func(eh parser.ExprHelper, target ast.Expr, args []ast.Expr) (ast.Expr, *common.Error) {
		if target != nil {
			return eh.NewMemberCall(m.Name, target, args...), nil
		}
		return eh.NewCall(m.Name, args...), nil
	}
	opts := []cel.MacroOpt{}
	if m.Doc != "" {
		opts = append(opts, cel.MacroDocs(m.Doc))
	}
	switch {
	case m.VarArg && m.Receiver:
		return cel.ReceiverVarArgMacro(m.Name, noop, opts...)
	case m.VarArg:
		return cel.GlobalVarArgMacro(m.Name, noop, opts...)
	case m.Receiver:
		return cel.ReceiverMacro(m.Name, m.FixedArity, noop, opts...)
	default:
		return cel.GlobalMacro(m.Name, m.FixedArity, noop, opts...)
	}
}
