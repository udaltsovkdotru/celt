// Package config parses the celt environment declaration (.celt.yml) into
// cel-go environment options. It is intentionally generic: it carries no
// knowledge of any particular consumer's message vocabulary, types, functions
// or macros — everything is supplied by the caller's YAML.
package config

import (
	"fmt"
	"strings"

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
	var root map[string]yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse env: %w", err)
	}
	env := &Env{}
	node, ok := root["env"]
	if !ok {
		return env, nil
	}
	// Reject unknown env keys (e.g. "variablez") so a mistyped key is not
	// silently left unapplied. The node is decoded directly (not re-encoded and
	// reparsed) so a YAML alias defined elsewhere in the document resolves;
	// re-encoding the node would drop the anchor definitions and fail on the
	// alias.
	if err := checkEnvKeys(&node); err != nil {
		return nil, err
	}
	if err := node.Decode(env); err != nil {
		return nil, fmt.Errorf("parse env: %w", err)
	}
	if err := env.validate(); err != nil {
		return nil, err
	}
	return env, nil
}

// checkEnvKeys rejects a top-level env key that is not a recognized section, so
// a mistyped section name (e.g. "variablez") is reported instead of silently
// leaving the intended settings unapplied.
func checkEnvKeys(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		switch key {
		case "container", "abbreviations", "extensions", "variables", "functions", "types", "macros":
		default:
			return fmt.Errorf("parse env: unknown key %q", key)
		}
	}
	return nil
}

func (e *Env) validate() error {
	if err := e.validateVariables(); err != nil {
		return err
	}
	if err := e.validateFunctions(); err != nil {
		return err
	}
	if err := e.validateTypes(); err != nil {
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

func (e *Env) validateTypes() error {
	if e.Types == nil {
		return nil
	}
	seenMsg := map[string]bool{}
	for _, m := range e.Types.Messages {
		// Registration strips a leading dot from the name alias; validate
		// against the same normalized name so aliases like "native.Account"
		// and ".native.Account" are detected as duplicates.
		name := strings.TrimPrefix(m.Name, ".")
		if seenMsg[name] {
			return fmt.Errorf("env.types.messages[%q]: duplicate message name", m.Name)
		}
		seenMsg[name] = true
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
		if err := validateMacro(&m); err != nil {
			return err
		}
	}
	return nil
}

// validateMacro checks the non-duplicate constraints of a single macro.
func validateMacro(m *Macro) error {
	if len(m.Overloads) == 0 && len(m.Results) == 0 {
		return fmt.Errorf("env.macros[%q]: at least one of overloads or results is required", m.Name)
	}
	if err := validateMacroOverloads(m); err != nil {
		return err
	}
	for _, r := range m.Results {
		if r == "" {
			return fmt.Errorf("env.macros[%q]: empty result type", m.Name)
		}
	}
	if len(m.Results) > 1 {
		// CEL overloads are keyed by argument signature, so a macro with
		// unconstrained (dyn) arguments can expose only one output type;
		// several would collide. Distinct signatures require explicit
		// overloads instead.
		return fmt.Errorf("env.macros[%q]: multiple results with unconstrained arguments are "+
			"unsupported; use explicit overloads for distinct signatures", m.Name)
	}
	if !m.VarArg && m.FixedArity < 0 {
		return fmt.Errorf("env.macros[%q]: fixed_arity must be >= 0", m.Name)
	}
	if len(m.Overloads) > 0 && !m.VarArg {
		if err := validateOverloadArities(m); err != nil {
			return err
		}
	}
	return validateMacroResultCollisions(m)
}

// validateMacroOverloads checks the per-overload constraints of a macro.
func validateMacroOverloads(m *Macro) error {
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
		// A receiver macro's overloads are member overloads, so the
		// receiver is always Args[0]; an overload with no arguments can
		// never match a receiver call, even when vararg.
		if m.Receiver && len(ov.Args) == 0 {
			return fmt.Errorf("env.macros[%q] overload %q: receiver macro overload must declare a receiver argument", m.Name, ov.ID)
		}
	}
	return nil
}

// validateOverloadArities checks that each overload's argument count agrees
// with a fixed_arity macro. A fixed-arity macro's overloads must agree with
// fixed_arity: the passthrough macro accepts exactly fixed_arity arguments, so
// an overload of a different arity could never match a parsed call.
func validateOverloadArities(m *Macro) error {
	for _, ov := range m.Overloads {
		n := len(ov.Args)
		if m.Receiver {
			n--
		}
		if n != m.FixedArity {
			return fmt.Errorf("env.macros[%q] overload %q: argument count (%d) disagrees with fixed_arity (%d)", m.Name, ov.ID, n, m.FixedArity)
		}
	}
	return nil
}

// validateMacroResultCollisions checks that unconstrained results do not
// collide with explicit overloads at the same arity.
func validateMacroResultCollisions(m *Macro) error {
	if len(m.Overloads) == 0 || len(m.Results) == 0 {
		return nil
	}
	// A macro combining exact overloads with unconstrained results builds
	// a result overload at each accepted arity; when one collides with an
	// explicit overload (same arity, dyn args overlap any type), cel-go
	// rejects the environment. Catch it here with a clear message.
	resArities := map[int]bool{}
	for _, n := range macroArities(m) {
		resArities[n] = true
	}
	for _, ov := range m.Overloads {
		n := len(ov.Args)
		if m.Receiver {
			n--
		}
		if resArities[n] {
			return fmt.Errorf("env.macros[%q] overload %q: result signature collides with an "+
				"unconstrained result at arity %d; use only overloads or only results", m.Name, ov.ID, n)
		}
	}
	return nil
}
