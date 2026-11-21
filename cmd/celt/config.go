package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"cel.dev/cel-go/cel"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtfmt"
	fmtrules "github.com/udaltsovkdotru/celt/celtfmt/rules"
	"github.com/udaltsovkdotru/celt/celtlint"
	lintrules "github.com/udaltsovkdotru/celt/celtlint/rules"
	configenv "github.com/udaltsovkdotru/celt/pkg/config"
)

// configFileName is one of the config files celt looks for, walking up from
// the current directory. Supported: .celt.yaml, .celt.yml, celt.yaml,
// celt.yml.
var configFileNames = []string{".celt.yaml", ".celt.yml", "celt.yaml", "celt.yml"}

// severity names accepted in the lint section.
var severityNames = map[string]celtlint.Level{
	"none":  celtlint.LevelOff,
	"warn":  celtlint.LevelWarn,
	"error": celtlint.LevelError,
}

// knownLintRules is the set of rule names the config file may configure,
// derived from the built-in lint rules so a mistyped name is rejected rather
// than silently ignored.
var knownLintRules = func() map[string]bool {
	m := map[string]bool{}
	for _, r := range lintrules.All() {
		m[r.Meta().Name] = true
	}
	return m
}()

// configurableLintRules is the set of lint rules that accept custom
// configuration, so a settings block under a non-configurable rule (which
// would otherwise ignore it) is rejected instead of silently unapplied.
var configurableLintRules = func() map[string]bool {
	m := map[string]bool{}
	for _, r := range lintrules.All() {
		if _, ok := r.(celtlint.Configurable); ok {
			m[r.Meta().Name] = true
		}
	}
	return m
}()

// fileConfig is the on-disk YAML shape.
type fileConfig struct {
	Fmt  map[string]yaml.Node `yaml:"fmt"`
	Lint map[string]yaml.Node `yaml:"lint"`
}

// config implements celtfmt.Config (the enabled format rules), celtlint's
// ConfigProvider (per-rule lint levels + custom config), and carries the
// declarative CEL environment parsed from the env section.
type config struct {
	fmtRules []celtfmt.FormatRule
	// fmtEnabled records a format rule's explicit enabled/disabled state from
	// the config file, e.g. the "break-operators" toggle.
	fmtEnabled map[string]bool
	lint       map[string]lintRuleCfg
	env        *configenv.Env
}

type lintRuleCfg struct {
	level    celtlint.Level
	hasLevel bool
	raw      []byte
}

// envOptions builds the CEL environment options from the declarative env
// section (extensions, container, variables, functions, macros, types) and,
// when present, the descriptor type set.
func (c *config) envOptions(fdset *descriptorpb.FileDescriptorSet) ([]cel.EnvOption, error) {
	var opts []cel.EnvOption
	if c.env != nil {
		o, err := c.env.Options()
		if err != nil {
			return nil, err
		}
		opts = append(opts, o...)
	}
	if fdset != nil {
		opts = append([]cel.EnvOption{cel.TypeDescs(fdset)}, opts...)
	}
	return opts, nil
}

// Rules implements celtfmt.Config.
func (c *config) Rules() []celtfmt.FormatRule { return c.fmtRules }

// Enabled implements celtfmt.RuleConfig, reporting a format rule's explicit
// enabled/disabled override from the config file.
func (c *config) Enabled(name string) (enabled, configured bool) {
	if v, ok := c.fmtEnabled[name]; ok {
		return v, true
	}
	return false, false
}

// Level implements celtlint.ConfigProvider.
func (c *config) Level(name string) (celtlint.Level, bool) {
	if r, ok := c.lint[name]; ok {
		return r.level, r.hasLevel
	}
	return 0, false
}

// Raw implements celtlint.ConfigProvider.
func (c *config) Raw(name string) ([]byte, bool) {
	if r, ok := c.lint[name]; ok {
		return r.raw, r.raw != nil
	}
	return nil, false
}

// loadConfig loads settings from the first config file found (or defaults if
// none present) into a single *config that implements celtfmt.Config and
// celtlint.ConfigProvider. startDir is the directory to begin the upward search
// from; an empty string means the process working directory.
func loadConfig(startDir string) (*config, error) {
	path := findConfig(startDir)
	cfg := &config{lint: map[string]lintRuleCfg{}}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.parse(data); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *config) parse(data []byte) error {
	// A second YAML document would be silently ignored by yaml.Unmarshal,
	// letting an unknown key in it slip past validation. The config format
	// holds a single document, so reject any additional one.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docCount int
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
		docCount++
		if docCount > 1 {
			return fmt.Errorf("parse config: multiple YAML documents are not supported")
		}
	}
	// Reject unknown top-level sections (e.g. "linnt") so a mistyped key does
	// not leave intended settings silently unapplied.
	if err := rejectUnknownKeys(data, map[string]bool{"fmt": true, "lint": true, "env": true}, "config"); err != nil {
		return err
	}
	var file fileConfig
	if err := yaml.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	env, err := configenv.ParseEnv(data)
	if err != nil {
		return err
	}
	c.env = env
	for _, name := range sortedKeys(file.Fmt) {
		node := file.Fmt[name]
		if err := c.parseFmtRule(name, &node); err != nil {
			return err
		}
	}
	for _, name := range sortedKeys(file.Lint) {
		node := file.Lint[name]
		if err := c.parseLintRule(name, &node); err != nil {
			return err
		}
	}
	return nil
}

// sortedKeys returns a map's keys in sorted order so rule processing (and thus
// the fmt rule list) is deterministic across runs.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (c *config) parseFmtRule(name string, node *yaml.Node) error {
	switch name {
	case "indent":
		var s string
		if err := node.Decode(&s); err != nil {
			return fmt.Errorf("fmt.%s: %w", name, err)
		}
		c.fmtRules = append(c.fmtRules, fmtrules.NewIndentRule(s))
	case "width":
		var n int
		if err := node.Decode(&n); err != nil {
			return fmt.Errorf("fmt.%s: %w", name, err)
		}
		c.fmtRules = append(c.fmtRules, fmtrules.NewWidthRule(n))
	case "break-operators":
		var enabled bool
		if err := node.Decode(&enabled); err != nil {
			return fmt.Errorf("fmt.%s: %w", name, err)
		}
		c.fmtRules = append(c.fmtRules, fmtrules.NewBreakOperatorsRule())
		if c.fmtEnabled == nil {
			c.fmtEnabled = map[string]bool{}
		}
		c.fmtEnabled["break-operators"] = enabled
	default:
		return fmt.Errorf("fmt.%s: unknown format rule", name)
	}
	return nil
}

func (c *config) parseLintRule(name string, node *yaml.Node) error {
	// Dereference a YAML alias so a severity shared via an anchor (e.g.
	// `misspell: *sev`) is seen as its scalar value rather than an alias node.
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	if !knownLintRules[name] {
		return fmt.Errorf("lint.%s: unknown lint rule", name)
	}
	rc := lintRuleCfg{}
	// Support the shorthand <name>: <severity>, keeping default config.
	if node.Kind == yaml.ScalarNode {
		sev := strings.TrimSpace(node.Value)
		lvl, ok := severityNames[sev]
		if !ok {
			return fmt.Errorf("lint.%s: unknown severity %q", name, sev)
		}
		rc.level = lvl
		rc.hasLevel = true
		c.lint[name] = rc
		return nil
	}
	// Full form: a mapping with level and optional custom config. Decode the
	// node in the original document's anchor context so YAML aliases (e.g.
	// `lint: {compile: &sev error, misspell: *sev}`) resolve; re-encoding the
	// raw node before decoding would drop the anchor definitions and fail on
	// the alias.
	var m map[string]any
	if err := node.Decode(&m); err != nil {
		return fmt.Errorf("lint.%s: %w", name, err)
	}
	if lvlVal, ok := m["level"]; ok {
		lvlStr, ok := lvlVal.(string)
		if !ok {
			return fmt.Errorf("lint.%s: level must be a severity name", name)
		}
		lvl, ok := severityNames[strings.TrimSpace(lvlStr)]
		if !ok {
			return fmt.Errorf("lint.%s: unknown severity %q", name, lvlStr)
		}
		rc.level = lvl
		rc.hasLevel = true
		// The reserved "level" key is not part of the rule's custom config.
		delete(m, "level")
	}
	// If nothing remains besides the (now-removed) level key, there is no
	// custom config: leave raw nil so the rule's declared defaults are kept.
	if len(m) == 0 {
		rc.raw = nil
	} else {
		// A rule that cannot consume custom settings must reject them here;
		// otherwise they would be silently ignored. Name the offending key.
		if !configurableLintRules[name] {
			keys := sortedKeys(m)
			return fmt.Errorf("lint.%s: unknown config key %q", name, keys[0])
		}
		// Re-encode the resolved (alias-expanded) mapping so the rule receives
		// valid, self-contained config.
		resolved, err := yaml.Marshal(m)
		if err != nil {
			return fmt.Errorf("lint.%s: %w", name, err)
		}
		rc.raw = resolved
	}
	c.lint[name] = rc
	return nil
}

// rejectUnknownKeys reports the first top-level key of data that is not in
// known, so a misspelled section is rejected rather than ignored.
func rejectUnknownKeys(data []byte, known map[string]bool, ctx string) error {
	var m map[string]yaml.Node
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil // the structured decode below reports the parse error
	}
	for k := range m {
		if !known[k] {
			return fmt.Errorf("%s: unknown key %q", ctx, k)
		}
	}
	return nil
}

// findConfig searches upward from startDir (the process working directory when
// empty) for the first project config file.
func findConfig(startDir string) string {
	dir := startDir
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return ""
		}
	} else if !filepath.IsAbs(dir) {
		// Resolve a relative start directory against the working directory so
		// the upward walk reaches the project root instead of stopping at ".".
		abs, err := filepath.Abs(dir)
		if err != nil {
			return ""
		}
		dir = abs
	}
	for {
		for _, name := range configFileNames {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

type formatMode int

const (
	formatPrint formatMode = iota
	formatWrite
	formatCheck
)

// runFormat implements the "format" subcommand.
func runFormat(args []string) int {
	fs := flag.NewFlagSet("format", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	write := fs.Bool("write", false, "write result to the source file instead of stdout")
	check := fs.Bool("check", false, "report files that are not formatted and exit 1 (no writes)")
	fs.Usage = func() { subUsage(fs, "usage: celt format [flags]", "Format .cel files.") }
	if err := fs.Parse(args); err != nil {
		return helpExit(err)
	}

	if *write && *check {
		fmt.Fprintln(os.Stderr, "format: --write and --check are mutually exclusive")
		fs.Usage()
		return 2
	}
	mode := formatPrint
	switch {
	case *write:
		mode = formatWrite
	case *check:
		mode = formatCheck
	}
	files := fs.Args()
	if len(files) == 0 {
		files = []string{"-"}
	}

	status := 0
	dirty := 0
	for _, f := range files {
		wasDirty, err := formatOneFile(f, mode)
		if err != nil {
			status = 1
			continue
		}
		if wasDirty {
			fmt.Fprintln(os.Stderr, f)
			dirty++
		}
	}
	if dirty > 0 {
		// --check reports files that need formatting and exits 1.
		if mode == formatCheck {
			fmt.Fprintf(os.Stderr, "format: %d file(s) not formatted\n", dirty)
			return 1
		}
		// --write reports files it rewrote as a successful operation (exit 0).
		fmt.Fprintf(os.Stderr, "format: %d file(s) rewritten\n", dirty)
	}
	return status
}

// formatOneFile formats a single file (or stdin) in the given mode, returning
// whether it was dirty and any error (already reported to stderr).
func formatOneFile(f string, mode formatMode) (wasDirty bool, err error) {
	if f == "-" && mode == formatWrite {
		// Reject before reading stdin: there is no file to write on stdin,
		// and waiting on an open stdin would hang the command.
		fmt.Fprintln(os.Stderr, "format: -: --write requires a file, not stdin")
		return false, errors.New("stdin write not supported")
	}
	// Resolve the format config from the target file's project, not the
	// launch directory, so an absolute path formatted from a parent uses
	// the same style as when launched from the project. Stdin uses the
	// working directory. The applicable config's validation errors must
	// surface before any file is written and before stdin is read, so an
	// invalid config is reported without blocking on an open stdin pipe.
	fileCfg, ferr := loadConfig(filepath.Dir(f))
	if ferr != nil {
		fmt.Fprintf(os.Stderr, "format: %s: %v\n", f, ferr)
		return false, ferr
	}
	src, rerr := readInput(f)
	if rerr != nil {
		fmt.Fprintf(os.Stderr, "format: %s: %v\n", f, rerr)
		return false, rerr
	}
	return formatFile(f, src, fileCfg, mode)
}

func readInput(f string) ([]byte, error) {
	if f == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(f)
}

func formatFile(f string, src []byte, cfg celtfmt.Config, mode formatMode) (wasDirty bool, err error) {
	formatted, ferr := celtfmt.Format(string(src), cfg)
	if ferr != nil {
		return false, ferr
	}
	changed := formatted != string(src)
	switch mode {
	case formatCheck:
		return changed, nil
	case formatWrite:
		if changed {
			return true, celtfmt.WriteFile(f, []byte(formatted))
		}
		return false, nil
	default:
		// Report a failed stdout write (e.g. redirected to a full device) as an
		// error rather than claiming success with no output produced.
		if _, werr := fmt.Print(formatted); werr != nil {
			return false, fmt.Errorf("write stdout: %w", werr)
		}
		return false, nil
	}
}
