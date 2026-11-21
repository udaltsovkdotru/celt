package celtlint

import (
	"sync"
	"testing"

	"cel.dev/cel-go/cel"
)

func TestAnalyzerConcurrentFirstUse(t *testing.T) {
	env, err := cel.NewEnv(cel.Variable("name", cel.StringType))
	if err != nil {
		t.Fatal(err)
	}
	// The rule has no shared mutable state and does not wait for other calls:
	// analyzers are free to serialize rule execution.
	rule := &lnPlainRule{
		RuleMeta: RuleMeta{Name: "index-check", Defaults: RuleParams{Level: LevelError}},
		check: func(ctx *Context) {
			if ctx.Index == nil {
				ctx.Report(&Finding{Code: "missing-index"})
			}
		},
	}
	a, err := New(env, WithRules(rule))
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	start := make(chan struct{})
	results := make(chan []Finding, workers)
	var ready sync.WaitGroup
	ready.Add(workers)
	for range workers {
		go func() {
			ready.Done()
			<-start
			results <- a.Analyze("concurrent.cel", `name == "test"`)
		}()
	}
	ready.Wait()
	close(start)
	for range workers {
		if findings := <-results; len(findings) != 0 {
			t.Errorf("Analyze returned findings: %+v", findings)
		}
	}
}
