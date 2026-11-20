// Command celt is a generic CEL linter/formatter built on the celt framework.
// It is not tied to any particular message vocabulary: the environment is
// built from the standard CEL extension libraries, optionally backed by a
// protobuf FileDescriptorSet (--descriptor) to resolve message fields and
// enums. Rules and actions are configured in .celt.yml (fmt/lint sections).
// Any CEL that type-checks against that environment can be linted, formatted
// and served over LSP via the lint, format and lsp subcommands.
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

// buildAnalyzer loads the config, descriptor set and CEL env, and constructs
// the lint analyzer shared by the lint and lsp subcommands. The loaded config
// is returned for consumers that need it (e.g. the LSP format config).
func buildAnalyzer(descriptor *string) (*celtlint.Analyzer, *config, error) {
	cfg, err := loadConfig()
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
	opts := []celtlint.Option{celtlint.WithConfigProvider(cfg), celtlint.WithRules(lintrules.All()...)}
	if fdset != nil {
		opts = append(opts, celtlint.WithDescriptor(fdset))
	}
	analyzer, err := celtlint.New(env, opts...)
	if err != nil {
		return nil, nil, err
	}
	return analyzer, cfg, nil
}

//nolint:gocyclo,funlen // top-level CLI flow: setup, per-file loop, and fix/report branches
func runLint(args []string) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "directory containing .cel files")
	descriptor := fs.String("descriptor", "", "path to the protobuf FileDescriptorSet (optional)")
	fix := fs.Bool("fix", false, "apply suggested fixes in place")
	fs.Usage = func() { subUsage(fs, "usage: celt lint [flags]", "Lint .cel files in a directory.") }
	if err := fs.Parse(args); err != nil {
		return helpExit(err)
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "celt lint: --dir is required")
		fs.Usage()
		return 2
	}

	analyzer, _, err := buildAnalyzer(descriptor)
	if err != nil {
		return fatalf("lint", err)
	}

	files, err := celFiles(*dir)
	if err != nil {
		return fatalf("lint", err)
	}

	results := make([]celtlint.LintedSource, 0, len(files))
	hadError := false
	for _, f := range files {
		content, rerr := os.ReadFile(f)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "celt lint: %s: %v\n", f, rerr)
			return 1
		}
		findings := analyzer.Analyze(f, string(content))
		for i := range findings {
			if findings[i].IsError() {
				hadError = true
			}
		}
		results = append(results, celtlint.LintedSource{Name: f, Content: string(content), Findings: findings})
	}

	if *fix {
		fixed := 0
		for i := range results {
			newSrc, _, applied := celtlint.ApplyFixes(results[i].Content, results[i].Findings)
			if !applied {
				continue
			}
			if err := celtfmt.WriteFile(results[i].Name, []byte(newSrc)); err != nil {
				fmt.Fprintf(os.Stderr, "celt lint: fix %s: %v\n", results[i].Name, err)
				return 1
			}
			results[i].Content = newSrc
			fixed++
		}
		// Re-analyze the (possibly fixed) sources so unfixable findings still
		// affect the exit code, and render what remains after fixing.
		hadError = false
		for i := range results {
			results[i].Findings = analyzer.Analyze(results[i].Name, results[i].Content)
			for j := range results[i].Findings {
				if results[i].Findings[j].IsError() {
					hadError = true
				}
			}
		}
		fmt.Fprint(os.Stderr, celtlint.RenderAll(results))
		fmt.Fprintf(os.Stderr, "fixed %d file(s)\n", fixed)
		if hadError {
			return 1
		}
		return 0
	}

	fmt.Fprint(os.Stderr, celtlint.RenderAll(results))
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
	fmt.Fprintln(os.Stderr, celtlint.Summary(stats, filepath.Base(os.Args[0])))
	if hadError {
		return 1
	}
	return 0
}

func runLSP(args []string) int {
	fs := flag.NewFlagSet("lsp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	descriptor := fs.String("descriptor", "", "path to the protobuf FileDescriptorSet (optional)")
	fs.Usage = func() { subUsage(fs, "usage: celt lsp [flags]", "Run as a language server over stdio.") }
	if err := fs.Parse(args); err != nil {
		return helpExit(err)
	}

	analyzer, cfg, err := buildAnalyzer(descriptor)
	if err != nil {
		return fatalf("lsp", err)
	}
	if err := lsp.New(analyzer,
		lsp.WithFormatConfig(cfg),
		lsp.WithActions(actions.All(cfg)...),
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
