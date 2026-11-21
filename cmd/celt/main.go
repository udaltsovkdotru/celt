// Command celt is a generic CEL linter, formatter and language server built on
// the celt framework. It is not tied to any particular message vocabulary: the
// environment is declared in .celt.yml (the env section — variables, functions,
// macros and types, plus optional CEL extensions such as strings or optional),
// and optionally backed by a protobuf FileDescriptorSet (--descriptor) to
// resolve message fields and enums. Rules are configured in .celt.yml (fmt/lint
// sections); code actions are wired in by the CLI rather than configured there.
// CEL that type-checks against that environment can be linted via the lint
// subcommand; the format subcommand handles the constructs the formatter
// supports, and rejects a few it cannot yet preserve (e.g. optional syntax and
// comprehensions) rather than rewriting them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"

	"google.golang.org/protobuf/proto"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"cel.dev/cel-go/cel"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtlint"
	lintrules "github.com/udaltsovkdotru/celt/celtlint/rules"
	lsp "github.com/udaltsovkdotru/celt/celtls"
	"github.com/udaltsovkdotru/celt/celtls/actions"
)

func main() {
	if len(os.Args) < 2 {
		help(os.Stdout)
		return
	}
	switch os.Args[1] {
	case "lint":
		os.Exit(runLint(os.Args[2:]))
	case "format":
		os.Exit(runFormat(os.Args[2:]))
	case "lsp":
		os.Exit(runLSP(os.Args[2:]))
	case "help", "-h", "--help":
		help(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "celt: unknown command %q\n\n", os.Args[1])
		help(os.Stderr)
		os.Exit(2)
	}
}

// buildAnalyzer loads the config (searched from configDir, or the process
// working directory when empty), descriptor set and CEL env, and constructs
// the lint analyzer shared by the lint and lsp subcommands. The loaded config
// is returned for consumers that need it (e.g. the LSP format config).
func buildAnalyzer(descriptor *string, configDir string) (*celtlint.Analyzer, *config, error) {
	cfg, err := loadConfig(configDir)
	if err != nil {
		return nil, nil, err
	}
	fdset, err := loadDescriptorOpt(descriptor)
	if err != nil {
		return nil, nil, err
	}
	envOpts, err := cfg.envOptions(fdset)
	if err != nil {
		return nil, nil, err
	}
	env, err := cel.NewEnv(envOpts...)
	if err != nil {
		return nil, nil, err
	}
	opts := []celtlint.Option{celtlint.WithConfigProvider(cfg), celtlint.WithRules(lintrules.All()...), celtlint.WithFormatConfig(cfg)}
	if fdset != nil {
		opts = append(opts, celtlint.WithDescriptor(fdset))
	}
	analyzer, err := celtlint.New(env, opts...)
	if err != nil {
		return nil, nil, err
	}
	return analyzer, cfg, nil
}

// lintArgs holds the parsed `celt lint` flags.
type lintArgs struct {
	dir, descriptor string
	fix             bool
}

// parseLintFlags parses the `celt lint` flags. It returns a nil args with a
// non-zero exit code when parsing fails or a required flag is missing.
func parseLintFlags(args []string) (la *lintArgs, code int) {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "directory containing .cel files")
	descriptor := fs.String("descriptor", "", "path to the protobuf FileDescriptorSet (optional)")
	fix := fs.Bool("fix", false, "apply suggested fixes in place")
	fs.Usage = func() { subUsage(fs, "usage: celt lint [flags]", "Lint .cel files in a directory.") }
	if err := fs.Parse(args); err != nil {
		return nil, helpExit(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "celt lint: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return nil, 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "celt lint: --dir is required")
		fs.Usage()
		return nil, 2
	}
	return &lintArgs{dir: *dir, descriptor: *descriptor, fix: *fix}, 0
}

// analyzeFiles reads and analyzes each file, returning the linted sources, a
// flag that an error-severity finding was seen, and a non-zero exit code if a
// file could not be read.
func analyzeFiles(analyzer *celtlint.Analyzer, files []string) (results []celtlint.LintedSource, hadError bool, code int) {
	results = make([]celtlint.LintedSource, 0, len(files))
	hadError = false
	for _, f := range files {
		content, rerr := os.ReadFile(f)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "celt lint: %s: %v\n", f, rerr)
			return nil, false, 1
		}
		findings := analyzer.Analyze(f, string(content))
		for i := range findings {
			if findings[i].IsError() {
				hadError = true
			}
		}
		results = append(results, celtlint.LintedSource{Name: f, Content: string(content), Findings: findings})
	}
	return results, hadError, 0
}

// fixLintResults applies each file's suggested fixes, rejecting edits that
// introduce a new compile error. It returns the number of files fixed and a
// non-zero exit code if writing a fixed file fails.
func fixLintResults(analyzer *celtlint.Analyzer, results []celtlint.LintedSource) (fixed, code int) {
	fixed = 0
	for i := range results {
		newSrc, _, applied := celtlint.ApplyFixes(results[i].Content, results[i].Findings)
		if !applied {
			continue
		}
		// A candidate fix must not make a file more invalid. The compiler
		// is consulted directly (independently of whether the compile rule
		// is enabled), so an edit that introduces a NEW compile error is
		// rejected even when the original already has one (e.g. an
		// unrelated `&& bogus`) or when compile findings are disabled.
		if introducesNewCompileError(analyzer.CompileErrors(newSrc), analyzer.CompileErrors(results[i].Content)) {
			continue
		}
		if err := celtfmt.WriteFile(results[i].Name, []byte(newSrc)); err != nil {
			fmt.Fprintf(os.Stderr, "celt lint: fix %s: %v\n", results[i].Name, err)
			return fixed, 1
		}
		results[i].Content = newSrc
		fixed++
	}
	return fixed, 0
}

// reanalyze re-lints the (possibly fixed) in-memory sources, updating their
// findings in place, and reports whether any error-severity finding remains.
func reanalyze(analyzer *celtlint.Analyzer, results []celtlint.LintedSource) bool {
	hadError := false
	for i := range results {
		results[i].Findings = analyzer.Analyze(results[i].Name, results[i].Content)
		for j := range results[i].Findings {
			if results[i].Findings[j].IsError() {
				hadError = true
			}
		}
	}
	return hadError
}

// renderSummary prints the aggregate check/error/warning/fixable stats.
func renderSummary(results []celtlint.LintedSource, prog string) {
	var stats celtlint.Stats
	stats.Checked = len(results)
	for _, r := range results {
		for i := range r.Findings {
			if r.Findings[i].IsError() {
				stats.Errors++
			} else {
				stats.Warnings++
			}
			if r.Findings[i].IsFix() {
				stats.Fixable++
			}
		}
	}
	fmt.Fprintln(os.Stderr, celtlint.Summary(stats, prog))
}

// boolToExit maps an error-severity-present flag to the process exit code.
func boolToExit(hadError bool) int {
	if hadError {
		return 1
	}
	return 0
}

func runLint(args []string) int {
	la, code := parseLintFlags(args)
	if la == nil {
		return code
	}
	analyzer, _, err := buildAnalyzer(&la.descriptor, la.dir)
	if err != nil {
		return fatalf("lint", err)
	}
	files, err := celFiles(la.dir)
	if err != nil {
		return fatalf("lint", err)
	}
	results, hadError, code := analyzeFiles(analyzer, files)
	if code != 0 {
		return code
	}
	if la.fix {
		fixed, code := fixLintResults(analyzer, results)
		if code != 0 {
			return code
		}
		hadError = reanalyze(analyzer, results)
		fmt.Fprint(os.Stderr, celtlint.RenderAll(results))
		fmt.Fprintf(os.Stderr, "fixed %d file(s)\n", fixed)
		return boolToExit(hadError)
	}
	fmt.Fprint(os.Stderr, celtlint.RenderAll(results))
	renderSummary(results, filepath.Base(os.Args[0]))
	return boolToExit(hadError)
}

func runLSP(args []string) int {
	fs := flag.NewFlagSet("lsp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	descriptor := fs.String("descriptor", "", "path to the protobuf FileDescriptorSet (optional)")
	fs.Usage = func() { subUsage(fs, "usage: celt lsp [flags]", "Run as a language server over stdio.") }
	if err := fs.Parse(args); err != nil {
		return helpExit(err)
	}

	analyzer, cfg, err := buildAnalyzer(descriptor, "")
	if err != nil {
		return fatalf("lsp", err)
	}
	// Rebuild the analyzer for the workspace chosen at initialize time, so a
	// server launched outside the project still uses the workspace's .celt.yml
	// (variables/descriptors/rules) rather than only the launcher's config.
	builder := func(dir string) (lsp.Analyzer, celtfmt.Config, []lsp.Action, error) {
		a, c, err := buildAnalyzer(descriptor, dir)
		if err != nil {
			return nil, nil, nil, err
		}
		return a, c, actions.All(c), nil
	}
	if err := lsp.New(analyzer,
		lsp.WithFormatConfig(cfg),
		lsp.WithActions(actions.All(cfg)...),
		lsp.WithAnalyzerBuilder(builder),
		lsp.WithName("celt"),
	).Run(); err != nil {
		return fatalf("lsp", err)
	}
	return 0
}

func help(out io.Writer) {
	fmt.Fprintln(out, "usage: celt <command> [flags]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "A generic CEL linter and formatter.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "commands:")
	fmt.Fprintln(out, "  lint     lint .cel files (optionally apply fixes)")
	fmt.Fprintln(out, "  format   format .cel files")
	fmt.Fprintln(out, "  lsp      run as a language server over stdio")
	fmt.Fprintln(out, "  help     show this help")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run `celt <command> --help` for command-specific flags.")
}

// subUsage prints a subcommand's synopsis and flags to its flag set output.
func subUsage(fs *flag.FlagSet, synopsis, desc string) {
	out := fs.Output()
	fmt.Fprintln(out, synopsis)
	fmt.Fprintln(out)
	fmt.Fprintln(out, desc)
	fmt.Fprintln(out)
	printFlags(out, fs)
}

// printFlags renders registered flags with double-dash long names.
func printFlags(out io.Writer, fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		fmt.Fprintf(out, "  --%s", f.Name)
		if name, _ := flag.UnquoteUsage(f); name != "" {
			fmt.Fprintf(out, " %s", name)
		}
		fmt.Fprintf(out, "\n    \t%s", f.Usage)
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
			fmt.Fprintf(out, " (default %s)", f.DefValue)
		}
		fmt.Fprintln(out)
	})
}

func helpExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

// introducesNewCompileError reports whether newErrs introduces a compiler error
// that was not already present at the same frequency in oldErrs. An error whose
// message never appeared before is new, as is an error that appears more often
// than it did before (e.g. a second incompatible ternary). When the counts all
// match, the fix neither adds nor removes an error, so it is allowed. Comparing
// occurrences rather than distinct messages keeps a candidate from slipping
// through when the original file already contained the same kind of error.
func introducesNewCompileError(newErrs, oldErrs []string) bool {
	oldCount := make(map[string]int, len(oldErrs))
	for _, e := range oldErrs {
		oldCount[e]++
	}
	newCount := make(map[string]int, len(newErrs))
	for _, e := range newErrs {
		newCount[e]++
	}
	for msg, n := range newCount {
		if n > oldCount[msg] {
			return true
		}
	}
	return false
}

func fatalf(cmd string, err error) int {
	fmt.Fprintf(os.Stderr, "celt %s: %v\n", cmd, err)
	return 1
}

func loadDescriptorOpt(p *string) (*descriptorpb.FileDescriptorSet, error) {
	if *p == "" {
		return nil, nil
	}
	return loadDescriptor(*p)
}

func loadDescriptor(path string) (*descriptorpb.FileDescriptorSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fdset := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, fdset); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return fdset, nil
}

func celFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(d.Name()) == ".cel" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}
