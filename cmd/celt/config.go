package main

import (
	"bytes"
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
// celtlint.ConfigProvider.
func loadConfig() (*config, error) {
	path := findConfig()
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
	// Full form: a mapping with level and optional custom config.
	var raw bytes.Buffer
	enc := yaml.NewEncoder(&raw)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return fmt.Errorf("lint.%s: %w", name, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("lint.%s: %w", name, err)
	}
	rc.raw = raw.Bytes()

	var m map[string]yaml.Node
	if err := yaml.Unmarshal(rc.raw, &m); err != nil {
		return fmt.Errorf("lint.%s: %w", name, err)
	}
	if lvlNode, ok := m["level"]; ok {
		sev := strings.TrimSpace(lvlNode.Value)
		lvl, ok := severityNames[sev]
		if !ok {
			return fmt.Errorf("lint.%s: unknown severity %q", name, sev)
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
		rc.raw = stripKey(rc.raw, "level")
	}
	c.lint[name] = rc
	return nil
}

// stripKey re-marshals a YAML mapping without the given key.
func stripKey(data []byte, key string) []byte {
	var m map[string]yaml.Node
	if err := yaml.Unmarshal(data, &m); err != nil {
		return data
	}
	delete(m, key)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return data
	}
	if err := enc.Close(); err != nil {
		return data
	}
	return buf.Bytes()
}

func findConfig() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
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

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "format: %v\n", err)
		return 1
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
		src, rerr := readInput(f)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "format: %s: %v\n", f, rerr)
			status = 1
			continue
		}

		effMode := mode
		if f == "-" {
			effMode = formatPrint
		}
		wasDirty, ferr := formatFile(f, src, cfg, effMode)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "format: %s: %v\n", f, ferr)
			status = 1
			continue
		}
		if wasDirty {
			fmt.Fprintln(os.Stderr, f)
			dirty++
		}
	}
	if dirty > 0 {
		fmt.Fprintf(os.Stderr, "format: %d file(s) not formatted\n", dirty)
		return 1
	}
	return status
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
		fmt.Print(formatted)
		return false, nil
	}
}
