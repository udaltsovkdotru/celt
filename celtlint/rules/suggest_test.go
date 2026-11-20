package rules

import (
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtlint"
)

// String constants reused across the suggestion table rows so golangci goconst
// stays quiet about repeated literals.
const (
	sgUserName      = "userName"
	sgFieldName     = "name"
	sgEnumName      = "Color"
	sgColorRED      = "Color.RED"
	sgUndeclaredRef = "undeclared reference"

	sgUnknownIdent        = "unknown identifier"
	sgCompareVarsDirect   = "comparing variables directly"
	sgCompareVarsDiffType = "comparing variables of different types"
)

// sgFirstError returns the first compile error whose message contains the
// given substring, or nil when none matches.
func sgFirstError(iss *cel.Issues, contains string) *cel.Error {
	for _, e := range iss.Errors() {
		if strings.Contains(e.Message, contains) {
			return e
		}
	}
	return nil
}

// sgEnv builds an env with a handful of names so the suggestion logic has real
// candidates to match, and returns it together with the built index.
func sgEnv(t *testing.T) (*cel.Env, *celtindex.Index) {
	t.Helper()
	env, err := cel.NewEnv(
		cel.Variable(sgUserName, cel.StringType),
		cel.Variable("w", cel.ObjectType("sg.pkg.Widget")),
	)
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return env, celtindex.Build(env, &descriptorpb.FileDescriptorSet{})
}

// TestSuggestTokenAt verifies the word-at-position helper extracts the whole
// identifier surrounding a byte column and returns empty for punctuation,
// out-of-range columns and empty lines.
func TestSuggestTokenAt(t *testing.T) {
	tests := []struct {
		name string
		line string
		col  int
		want string
	}{
		{name: "start", line: sgUserName, col: 0, want: sgUserName},
		{name: "middle", line: sgUserName, col: 3, want: sgUserName},
		{name: "end", line: sgUserName, col: 7, want: sgUserName},
		{name: "second word", line: "a = b", col: 4, want: "b"},
		{name: "after space", line: "a user", col: 2, want: "user"},
		{name: "after dot", line: "foo.bar", col: 4, want: "bar"},
		{name: "punct column", line: "a = b", col: 2, want: ""},
		{name: "col past line", line: "abc", col: 10, want: ""},
		{name: "negative col", line: "abc", col: -1, want: ""},
		{name: "empty line", line: "", col: 0, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenAt(tc.line, tc.col); got != tc.want {
				t.Errorf("tokenAt(%q,%d) = %q, want %q", tc.line, tc.col, got, tc.want)
			}
		})
	}
}

// TestSuggestBacktickExtraction verifies backtick-quote normalization and the
// backtick content extractor, including the quoting of single-quoted error
// names into backticks.
func TestSuggestBacktickExtraction(t *testing.T) {
	if got := celtlint.BacktickQuotes("undeclared reference to 'foo'"); got != "undeclared reference to `foo`" {
		t.Errorf("BacktickQuotes = %q", got)
	}
	if got := celtlint.BtName("undeclared reference to `foo` (in container ``)"); got != "foo" {
		t.Errorf("BtName = %q, want foo", got)
	}
	if got := celtlint.BtName("no backticks here"); got != "" {
		t.Errorf("BtName(no backticks) = %q, want empty", got)
	}
	if got := celtlint.BtName(celtlint.BacktickQuotes("undefined field 'name'")); got != sgFieldName {
		t.Errorf("BtName(BacktickQuotes) = %q, want name", got)
	}
}

// TestSuggestIsNameError verifies the name-error classifier accepts every
// message family that drives a suggestion and rejects type/other errors.
func TestSuggestIsNameError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{name: sgUndeclaredRef, msg: "undeclared reference to 'x'", want: true},
		{name: "undefined field", msg: "undefined field 'y'", want: true},
		{name: "no such field", msg: "no such field", want: true},
		{name: "no matching overload", msg: "found no matching overload for 'f'", want: true},
		{name: "unknown function", msg: "unknown function 'g'", want: true},
		{name: "field selection", msg: "does not support field selection", want: true},
		{name: "type mismatch", msg: "expected type int but found string", want: false},
		{name: "empty", msg: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNameError(tc.msg); got != tc.want {
				t.Errorf("isNameError(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// TestSuggestNamespaceMember verifies a dotted namespace reference is resolved
// to "namespace.member" only when an identifier follows the dot.
func TestSuggestNamespaceMember(t *testing.T) {
	tests := []struct {
		name string
		line string
		ns   string
		want string
	}{
		{name: "member", line: sgColorRED, ns: sgEnumName, want: sgColorRED},
		{name: "with leading text", line: "x Color.RED", ns: sgEnumName, want: sgColorRED},
		{name: "no member after dot", line: "Color.", ns: sgEnumName, want: ""},
		{name: "namespace not present", line: "noColor here", ns: sgEnumName, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := namespaceMember(tc.line, tc.ns); got != tc.want {
				t.Errorf("namespaceMember(%q,%q) = %q, want %q", tc.line, tc.ns, got, tc.want)
			}
		})
	}
}

// TestSuggestOperatorRendersEqualHint verifies a token-recognition error for
// '=' renders an equality diff, and other tokens or non-recognition messages
// render nothing.
func TestSuggestOperatorRendersEqualHint(t *testing.T) {
	src := common.NewStringSource("x = 1", "t")
	e := &common.Error{Message: "token recognition error at: '='", Location: common.NewLocation(1, 2)}

	got := suggestOperator(src, e)
	if !strings.Contains(got, "x == 1") {
		t.Errorf("suggestOperator output missing replacement line: %q", got)
	}
	if !strings.Contains(got, "you might have meant to compare for equality") {
		t.Errorf("suggestOperator output missing help text: %q", got)
	}

	other := &common.Error{Message: "token recognition error at: '@'", Location: common.NewLocation(1, 1)}
	if got := suggestOperator(src, other); got != "" {
		t.Errorf("suggestOperator(@) = %q, want empty", got)
	}
	if got := suggestOperator(src, &common.Error{Message: "some other error", Location: common.NewLocation(1, 0)}); got != "" {
		t.Errorf("suggestOperator(non-token) = %q, want empty", got)
	}
	if got := suggestOperator(src, &common.Error{Message: "token recognition error at: '='", Location: common.NewLocation(5, 0)}); got != "" {
		t.Errorf("suggestOperator(off-line) = %q, want empty", got)
	}
}

// TestSuggestForVariable verifies suggestFor replaces a near-miss variable
// name with the closest real candidate and renders a diff hint.
func TestSuggestForVariable(t *testing.T) {
	env, idx := sgEnv(t)
	src := common.NewStringSource("userNmae", "t")
	_, iss := env.CompileSource(src)
	e := sgFirstError(iss, sgUndeclaredRef)
	if e == nil {
		t.Fatal("expected an undeclared reference error")
	}

	want := "hint: a variable with a similar name exists\n  |\n1 - userNmae\n1 + userName\n  |"
	if got := suggestFor(src, e, idx); got != want {
		t.Errorf("suggestFor = %q, want %q", got, want)
	}
}

// TestSuggestForUnknownReference verifies a token far from every candidate
// yields no suggestion.
func TestSuggestForUnknownReference(t *testing.T) {
	env, idx := sgEnv(t)
	src := common.NewStringSource("qqqqzz", "t")
	_, iss := env.CompileSource(src)
	e := sgFirstError(iss, sgUndeclaredRef)
	if e == nil {
		t.Fatal("expected an undeclared reference error")
	}

	if got := suggestFor(src, e, idx); got != "" {
		t.Errorf("suggestFor(qqqqzz) = %q, want empty", got)
	}
}

// TestSuggestForNonNameError verifies suggestFor returns empty for an error
// that is not a name error (e.g. a syntax error).
func TestSuggestForNonNameError(t *testing.T) {
	_, idx := sgEnv(t)
	src := common.NewStringSource("1 +", "t")
	e := &common.Error{Message: "mismatched input '<EOF>' expecting ')'", Location: common.NewLocation(1, 3)}

	if got := suggestFor(src, e, idx); got != "" {
		t.Errorf("suggestFor(syntax) = %q, want empty", got)
	}
}
