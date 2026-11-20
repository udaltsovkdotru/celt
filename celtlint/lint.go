package celtlint

import (
	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtplugin"
	"github.com/udaltsovkdotru/celt/celtsource"
)

// RuleParams is the configuration of a lint rule: its severity level and any
// rule-specific config. It is the default value a rule declares, and the shape
// into which overrides are merged.
type RuleParams struct {
	// Level is the severity a rule reports. LevelOff disables the rule.
	Level Level
	// Config is the rule's configuration as raw bytes. May be nil when the
	// rule has no config.
	Config []byte
}

// RuleMeta is the static metadata of a lint rule: its name, description and
// default configuration. Rules embed it (as a value) to satisfy Rule. Meta()
// returns RuleMeta via the shared celtplugin.Meta.
type RuleMeta = celtplugin.Meta[RuleParams]

type Rule interface {
	Meta() RuleMeta
	Check(ctx *Context)
}

// ConfigProvider supplies the per-rule configuration overrides. Consumers
// implement it (typically backed by their own config file) and pass it via
// WithConfigProvider.
type ConfigProvider interface {
	// Level returns the configured level override for a rule by name. The bool
	// reports whether the rule was explicitly configured.
	Level(name string) (Level, bool)
	// Raw returns the raw config bytes override for a rule, if it has custom
	// config.
	Raw(name string) ([]byte, bool)
}

// Configurable is implemented by a Rule that accepts per-rule configuration.
// The framework applies the rule's effective config (defaults merged with
// overrides) via ApplyConfig before the rule runs.
type Configurable interface {
	Rule
	celtplugin.Configurable
}

type Context struct {
	Env      *cel.Env
	Source   common.Source
	Ast      *cel.Ast
	Err      *cel.Issues
	Index    *celtindex.Index
	Findings []Finding
}

func (c *Context) CompileFailed() bool {
	return c.Err != nil && len(c.Err.Errors()) > 0
}

func (c *Context) Report(f *Finding) { c.Findings = append(c.Findings, *f) }

func (c *Context) ReportAll(fs []Finding) { c.Findings = append(c.Findings, fs...) }

type Option func(*Config)

type Config struct {
	fdset    *descriptorpb.FileDescriptorSet
	provider ConfigProvider
	rules    []Rule
}

func WithDescriptor(fdset *descriptorpb.FileDescriptorSet) Option {
	return func(c *Config) { c.fdset = fdset }
}

func WithRules(rules ...Rule) Option {
	return func(c *Config) { c.rules = append(c.rules, rules...) }
}

// WithConfigProvider supplies the per-rule configuration overrides.
func WithConfigProvider(p ConfigProvider) Option {
	return func(c *Config) { c.provider = p }
}

type Analyzer struct {
	env      *cel.Env
	fdset    *descriptorpb.FileDescriptorSet
	index    *celtindex.Index
	docs     *HoverDocs
	provider ConfigProvider
	rules    []Rule
	built    bool
}

func New(env *cel.Env, opts ...Option) (*Analyzer, error) {
	c := &Config{}
	for _, o := range opts {
		o(c)
	}
	a := &Analyzer{env: env, provider: c.provider, fdset: c.fdset}
	a.rules = append(a.rules, c.rules...)
	for _, r := range a.rules {
		cfg := a.effectiveConfig(r)
		if cr, ok := r.(Configurable); ok {
			if err := cr.ApplyConfig(cfg.Config); err != nil {
				return nil, err
			}
		}
	}
	return a, nil
}

// ensureBuilt builds the environment index and hover docs on first use,
// memoized. The CLI linter analyzes once per process, so a lazy build avoids
// the cost when no analysis happens; the long-lived LSP server triggers it on
// its first document, i.e. near startup.
func (a *Analyzer) ensureBuilt() {
	if a.built {
		return
	}
	a.built = true
	a.index = celtindex.Build(a.env, a.fdset)
	a.docs = LoadHoverDocs(a.fdset, a.env)
}

// effectiveConfig merges a rule's declared defaults with the provider
// overrides.
func (a *Analyzer) effectiveConfig(r Rule) RuleParams {
	meta := r.Meta()
	cfg := meta.Defaults
	if a.provider == nil {
		return cfg
	}
	if lvl, ok := a.provider.Level(meta.Name); ok {
		cfg.Level = lvl
	}
	if raw, ok := a.provider.Raw(meta.Name); ok {
		cfg.Config = raw
	}
	return cfg
}

func (a *Analyzer) Analyze(path, content string) []Finding {
	return a.AnalyzeSource(common.NewStringSource(content, path))
}

func (a *Analyzer) AnalyzeSource(src common.Source) []Finding {
	a.ensureBuilt()
	ctx := &Context{
		Env:    a.env,
		Source: src,
		Index:  a.index,
	}
	ctx.Ast, ctx.Err = a.env.CompileSource(src)
	for _, r := range a.rules {
		cfg := a.effectiveConfig(r)
		if cfg.Level == LevelOff {
			continue
		}
		before := len(ctx.Findings)
		r.Check(ctx)
		// Apply the rule's effective level to findings it did not explicitly
		// differentiate (i.e. findings the rule reported without a level).
		if a.provider != nil {
			if lvl, ok := a.provider.Level(r.Meta().Name); ok {
				for i := before; i < len(ctx.Findings); i++ {
					ctx.Findings[i].Level = lvl
				}
			}
		}
	}
	return ctx.Findings
}

func (a *Analyzer) Hover(content string, line, char int) string {
	a.ensureBuilt()
	if a.docs == nil || a.index == nil {
		return ""
	}
	name, ok := IdentifierAt(content, line, char)
	if !ok {
		return ""
	}
	if a.isMacro(name) {
		if mc, ok := a.macroCallAtCursor(content, line, char); ok {
			return renderMacroCallHover(mc, a.docs.macros[name])
		}
	}
	return a.docs.Lookup(name, a.receiverElemAt(content, line, char))
}

// isMacro reports whether name is registered as a macro in the env.
func (a *Analyzer) isMacro(name string) bool {
	for _, m := range a.env.Macros() {
		if m.Function() == name {
			return true
		}
	}
	return false
}

// macroCallAtCursor compiles content and resolves the macro invocation whose
// source range contains the cursor, if any.
func (a *Analyzer) macroCallAtCursor(content string, line, char int) (*MacroCall, bool) {
	off, _, _, ok := celtsource.LineByteColOffset(content, line, char)
	if !ok {
		return nil, false
	}
	ast, iss := a.env.Compile(content)
	if iss != nil && iss.Err() != nil {
		return nil, false
	}
	return MacroCallAt(ast.NativeRep(), celtsource.RuneOffsets(content), content, a.env, off)
}

func (a *Analyzer) receiverElemAt(content string, line, char int) string {
	off, lineStart, _, ok := celtsource.LineByteColOffset(content, line, char)
	if !ok {
		return ""
	}

	i := off
	for i > lineStart && celtsource.IsIdentByte(content[i-1]) {
		i--
	}
	if i < lineStart+1 || content[i-1] != '.' {
		return ""
	}

	j := i - 2
	for j >= lineStart && celtsource.IsIdentByte(content[j]) {
		j--
	}
	field := content[j+1 : i-1]
	if field == "" {
		return ""
	}
	return a.index.ReceiverElem(field)
}
