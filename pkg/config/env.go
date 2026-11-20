// Package config parses the celt environment declaration (.celt.yml) into
// cel-go environment options. It is intentionally generic: it carries no
// knowledge of any particular consumer's message vocabulary, types, functions
// or macros — everything is supplied by the caller's YAML.
package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Env is the declarative description of a CEL environment.
type Env struct {
	Container     string     `yaml:"container"`
	Abbreviations []string   `yaml:"abbreviations"`
	Extensions    []string   `yaml:"extensions"`
	Variables     []Variable `yaml:"variables"`
	Functions     []Function `yaml:"functions"`
	Macros        []Macro    `yaml:"macros"`
	Types         *Types     `yaml:"types"`
}

// Variable is a top-level CEL variable declaration.
type Variable struct {
	Name string `yaml:"name"`
	// Type is a CEL type expression, e.g. "int", "list<string>",
	// "map<string, dyn>".
	Type string `yaml:"type"`
	// TypeName references a declared message or enum type by its fully
	// qualified name, e.g. "native.Account". Mutually exclusive with Type.
	TypeName string `yaml:"type_name"`
	Doc      string `yaml:"doc"`
}

// Function is a host-provided function declaration with one or more overloads.
type Function struct {
	Name      string     `yaml:"name"`
	Doc       string     `yaml:"doc"`
	Overloads []Overload `yaml:"overloads"`
}

// Overload is a single function overload. For a member (receiver-style)
// function, Args[0] is the receiver type, matching cel.MemberOverload.
type Overload struct {
	ID       string   `yaml:"id"`
	Member   bool     `yaml:"member"`
	Args     []string `yaml:"args"`
	Result   string   `yaml:"result"`
	Examples []string `yaml:"examples"`
}

// Macro is a declarative, signature-only macro. It is registered so that the
// parser accepts calls to it and it is surfaced in completion, hover and
// misspell suggestions, but it is not expanded. A macro may declare exact
// input/output type pairs (Overloads) and/or a plain list of possible output
// types (Results); at least one of the two must be specified.
type Macro struct {
	Name string `yaml:"name"`
	Doc  string `yaml:"doc"`
	// Receiver selects receiver-style (member) syntax: target.name(args).
	Receiver bool `yaml:"receiver"`
	// FixedArity is the number of arguments (excluding the receiver) when
	// VarArg is false.
	FixedArity int  `yaml:"fixed_arity"`
	VarArg     bool `yaml:"vararg"`
	// Overloads, when set, map exact input types to an output type, like a
	// function overload. For a receiver-style macro, Args[0] is the receiver.
	Overloads []Overload `yaml:"overloads"`
	// Results, when set, list possible output types with unconstrained
	// arguments. At least one of Overloads or Results must be present.
	Results []string `yaml:"results"`
}

// Types declares native message and enum types.
type Types struct {
	Messages []Message `yaml:"messages"`
	Enums    []Enum    `yaml:"enums"`
}

// Message is a native message type declaration.
type Message struct {
	Name   string  `yaml:"name"`
	Fields []Field `yaml:"fields"`
}

// Field is a single field of a native message type.
type Field struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Doc  string `yaml:"doc"`
}

// Enum is a native enum type declaration.
type Enum struct {
	Name   string   `yaml:"name"`
	Values []string `yaml:"values"`
}

// ParseEnv parses an Env declaration from raw YAML. The top-level document is
// expected to be a mapping whose "env" key holds the environment; an empty
// document yields an empty Env.
func ParseEnv(data []byte) (*Env, error) {
	var root struct {
		Env Env `yaml:"env"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse env: %w", err)
	}
	if err := root.Env.validate(); err != nil {
		return nil, err
	}
	return &root.Env, nil
}

func (e *Env) validate() error {
	if err := e.validateVariables(); err != nil {
		return err
	}
	if err := e.validateFunctions(); err != nil {
		return err
	}
	return e.validateMacros()
}

func (e *Env) validateVariables() error {
	seenVar := map[string]bool{}
	for _, v := range e.Variables {
		if v.Name == "" {
			return fmt.Errorf("env.variables: missing name")
		}
		if v.Type == "" && v.TypeName == "" {
			return fmt.Errorf("env.variables[%q]: missing type or type_name", v.Name)
		}
		if v.Type != "" && v.TypeName != "" {
			return fmt.Errorf("env.variables[%q]: type and type_name are mutually exclusive", v.Name)
		}
		if seenVar[v.Name] {
			return fmt.Errorf("env.variables[%q]: duplicate", v.Name)
		}
		seenVar[v.Name] = true
	}
	return nil
}

func (e *Env) validateFunctions() error {
	seenFn := map[string]bool{}
	for _, fn := range e.Functions {
		if fn.Name == "" {
			return fmt.Errorf("env.functions: missing name")
		}
		if seenFn[fn.Name] {
			return fmt.Errorf("env.functions[%q]: duplicate", fn.Name)
		}
		seenFn[fn.Name] = true
		if len(fn.Overloads) == 0 {
			return fmt.Errorf("env.functions[%q]: no overloads", fn.Name)
		}
		seenOv := map[string]bool{}
		for _, ov := range fn.Overloads {
			if ov.ID == "" {
				return fmt.Errorf("env.functions[%q]: overload missing id", fn.Name)
			}
			if seenOv[ov.ID] {
				return fmt.Errorf("env.functions[%q]: duplicate overload id %q", fn.Name, ov.ID)
			}
			seenOv[ov.ID] = true
			if ov.Result == "" {
				return fmt.Errorf("env.functions[%q] overload %q: missing result", fn.Name, ov.ID)
			}
		}
	}
	return nil
}

func (e *Env) validateMacros() error {
	seenMacro := map[string]bool{}
	for _, m := range e.Macros {
		if m.Name == "" {
			return fmt.Errorf("env.macros: missing name")
		}
		if seenMacro[m.Name] {
			return fmt.Errorf("env.macros[%q]: duplicate", m.Name)
		}
		seenMacro[m.Name] = true
		if len(m.Overloads) == 0 && len(m.Results) == 0 {
			return fmt.Errorf("env.macros[%q]: at least one of overloads or results is required", m.Name)
		}
		seenOv := map[string]bool{}
		for _, ov := range m.Overloads {
			if ov.ID != "" && seenOv[ov.ID] {
				return fmt.Errorf("env.macros[%q]: duplicate overload id %q", m.Name, ov.ID)
			}
			if ov.ID != "" {
				seenOv[ov.ID] = true
			}
			if ov.Result == "" {
				return fmt.Errorf("env.macros[%q] overload %q: missing result type", m.Name, ov.ID)
			}
		}
		for _, r := range m.Results {
			if r == "" {
				return fmt.Errorf("env.macros[%q]: empty result type", m.Name)
			}
		}
		if !m.VarArg && m.FixedArity < 0 {
			return fmt.Errorf("env.macros[%q]: fixed_arity must be >= 0", m.Name)
		}
	}
	return nil
}
