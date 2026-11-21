package celtlint

import (
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtfmt"
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
	fmtCfg   celtfmt.Config
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

// WithFormatConfig supplies the celtfmt configuration used to render the
// desugared expansion of a macro invocation in hover. When nil, the default
// celtfmt layout applies.
func WithFormatConfig(cfg celtfmt.Config) Option {
	return func(c *Config) { c.fmtCfg = cfg }
}

type Analyzer struct {
	env       *cel.Env
	trackEnv  *cel.Env
	fdset     *descriptorpb.FileDescriptorSet
	index     *celtindex.Index
	docs      *HoverDocs
	provider  ConfigProvider
	rules     []Rule
	fmtCfg    celtfmt.Config
	buildOnce sync.Once
}

func New(env *cel.Env, opts ...Option) (*Analyzer, error) {
	c := &Config{}
	for _, o := range opts {
		o(c)
	}
	a := &Analyzer{env: env, provider: c.provider, fdset: c.fdset, fmtCfg: c.fmtCfg}
	// Macro expansion hover relies on the checked AST recording macro calls,
	// which only happens when the env was built with macro call tracking. Derive
	// a tracking env for the hover compile (preserving the caller's env for all
	// other analysis); if the extension fails, hover simply omits expansions.
	if te, err := env.Extend(cel.EnableMacroCallTracking()); err == nil {
		a.trackEnv = te
	}
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
// its first document, i.e. near startup. The build is synchronized so that
// concurrent first use (e.g. parallel LSP requests) builds the index/docs
// exactly once and no caller observes a partially-initialized analyzer.
func (a *Analyzer) ensureBuilt() {
	a.buildOnce.Do(func() {
		a.index = celtindex.Build(a.env, a.fdset)
		a.docs = LoadHoverDocs(a.fdset, a.env)
	})
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
		// Fill in the rule's effective level (its default, or a config-provider
		// override) for findings the rule reported without an explicit severity.
		// A rule that declares Defaults.Level therefore yields findings at that
		// level even when no provider overrides it; an explicitly-set finding
		// level is left as the rule chose.
		for i := before; i < len(ctx.Findings); i++ {
			if ctx.Findings[i].Level == 0 {
				ctx.Findings[i].Level = cfg.Level
			}
		}
	}
	return ctx.Findings
}

// CompileErrors compiles content with the environment's compiler, independent
// of whether the compile rule is enabled, and returns the unique compiler error
// messages. It is used to verify that a candidate fix does not introduce a new
// compile error: because the check bypasses the configurable compile rule, it
// still catches an invalid edit even when compile findings are disabled.
func (a *Analyzer) CompileErrors(content string) []string {
	if a.env == nil {
		return nil
	}
	_, iss := a.env.Compile(content)
	if iss == nil || len(iss.Errors()) == 0 {
		return nil
	}
	// One entry per error occurrence (not deduplicated by message): the fix
	// guard needs to tell a newly-added occurrence of an already-present error
	// (e.g. a second incompatible ternary) apart from the pre-existing ones.
	out := make([]string, 0, len(iss.Errors()))
	for _, e := range iss.Errors() {
		if e == nil || e.Message == "" {
			continue
		}
		out = append(out, e.Message)
	}
	return out
}

func (a *Analyzer) Hover(content string, line, char int) string {
	a.ensureBuilt()
	if a.docs == nil || a.index == nil {
		return ""
	}
	off, lineStart, _, ok := celtsource.LineByteColOffset(content, line, char)
	if !ok {
		return ""
	}
	name, ok := IdentifierAt(content, line, char)
	if !ok {
		return ""
	}
	if a.isMacro(name) {
		if mc, ok := a.macroCallAtCursor(content, line, char); ok {
			return renderMacroCallHover(mc, a.docs.macros[name], a.fmtCfg)
		}
	}
	// The token must be a genuine identifier reference: text inside a string
	// literal and a comprehension-local binding (which shadows any same-named
	// global) must not surface the global's documentation.
	if insideString(content, off) || a.isComprehensionLocal(content, off, name) {
		return ""
	}
	// Field hover is resolved by the receiver's type so fields sharing a bare
	// name across messages (Person.name vs Company.name) don't collide, and
	// indexed/complex receivers (`[person][0]`) resolve to the element type.
	if dot := receiverDotAt(content, lineStart, off); dot >= 0 {
		if recv, ok := receiverTextBefore(content, lineStart, dot); ok {
			if t := a.resolveReceiverType(recv); t != "" {
				if d, ok := a.docs.fieldFor(t, name); ok {
					return renderHover(d)
				}
			}
		}
	}
	return a.docs.Lookup(name, a.receiverElemAt(content, line, char))
}

// insideString reports whether byte offset off lies inside a string literal.
func insideString(content string, off int) bool {
	i := 0
	for i < off && i < len(content) {
		c := content[i]
		if c == '"' || c == '\'' {
			next := celtsource.SkipString(content, i)
			if off < next {
				return true
			}
			i = next
			continue
		}
		i++
	}
	return false
}

// isComprehensionLocal reports whether the identifier named name at byte offset
// off is bound as a comprehension local (iter/accu/second-iter variable), in
// which case it shadows any same-named global and must not surface that
// global's documentation.
func (a *Analyzer) isComprehensionLocal(content string, off int, name string) bool {
	if name == "" || a.env == nil {
		return false
	}
	ast := a.compileContent(content)
	if ast == nil {
		return false
	}
	native := ast.NativeRep()
	info := native.SourceInfo()
	ro := celtsource.RuneOffsets(content)
	found := false
	celtsource.Walk(native.Expr(), func(e celast.Expr) {
		if found {
			return
		}
		names, start, end := comprehensionBindScope(info, content, ro, e)
		if start < 0 || end <= start {
			return
		}
		for _, n := range names {
			if n == name && start <= off && off < end {
				found = true
				return
			}
		}
	})
	return found
}

// compileContent compiles content, falling back to a parsed (unchecked) AST when
// compilation fails but parsing succeeds, so comprehension-scope tracking still
// works for syntactically-valid-but-untyped expressions.
func (a *Analyzer) compileContent(content string) *cel.Ast {
	if checked, iss := a.env.Compile(content); iss == nil || iss.Err() == nil {
		return checked
	}
	if parsed, perr := a.env.Parse(content); parsed != nil && (perr == nil || perr.Err() == nil) {
		return parsed
	}
	return nil
}

// comprehensionBindScope returns the variable names a comprehension binds and
// the byte span [start, end) in which they are visible. A comprehension's own
// offset range is degenerate: its start is the argument-list `(` and there is
// no stop, so the scope runs from that opening paren to the matching close.
func comprehensionBindScope(info *celast.SourceInfo, content string, ro []int, e celast.Expr) (names []string, start, end int) {
	switch e.Kind() {
	case celast.ComprehensionKind:
		c := e.AsComprehension()
		if n := c.IterVar(); n != "" {
			names = append(names, n)
		}
		if c.HasIterVar2() {
			if n := c.IterVar2(); n != "" {
				names = append(names, n)
			}
		}
		if n := c.AccuVar(); n != "" {
			names = append(names, n)
		}
	case celast.CallKind:
		if isComprehensionMacroName(e.AsCall().FunctionName()) {
			args := e.AsCall().Args()
			if len(args) > 0 && args[0].Kind() == celast.IdentKind {
				if n := args[0].AsIdent(); n != "" {
					names = append(names, n)
				}
			}
		}
	}
	if len(names) == 0 {
		return names, -1, -1
	}
	start = -1
	if r, ok := info.GetOffsetRange(e.ID()); ok {
		start = celtsource.ByteOffset(ro, int(r.Start))
	}
	if start < 0 || start >= len(content) {
		return names, -1, -1
	}
	return names, start, macroScopeEndAt(content, start)
}

// isComprehensionMacroName reports whether name is a comprehension macro.
func isComprehensionMacroName(name string) bool {
	switch name {
	case "exists", "exists_one", "all", "filter", "map":
		return true
	}
	return false
}

// macroScopeEndAt returns the byte index just past the `)` balancing the `(` at
// open, skipping strings and comments.
func macroScopeEndAt(content string, open int) int {
	depth := 0
	for i := open; i < len(content); i++ {
		switch {
		case content[i] == '\'' || content[i] == '"':
			i = celtsource.SkipString(content, i) - 1
		case content[i] == '/' && i+1 < len(content) && content[i+1] == '/':
			for i < len(content) && content[i] != '\n' {
				i++
			}
			i--
		case content[i] == '(':
			depth++
		case content[i] == ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(content)
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
// source range contains the cursor, if any. The compile uses the tracking env
// (which records macro calls) so the expansion is recoverable; the caller's
// original env is used for all other analysis.
func (a *Analyzer) macroCallAtCursor(content string, line, char int) (*MacroCall, bool) {
	off, _, _, ok := celtsource.LineByteColOffset(content, line, char)
	if !ok {
		return nil, false
	}
	env := a.trackEnv
	if env == nil {
		env = a.env
	}
	ast, iss := env.Compile(content)
	if iss != nil && iss.Err() != nil {
		return nil, false
	}
	return MacroCallAt(ast.NativeRep(), celtsource.RuneOffsets(content), content, env, off)
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
