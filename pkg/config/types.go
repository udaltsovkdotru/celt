package config

import (
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"
)

// Options converts a native Types declaration into cel-go options. Messages
// are registered as native types via cel.Types with an explicit name alias, so
// the env's native type provider exposes their fields without a descriptor.
func (t *Types) Options() ([]cel.EnvOption, error) {
	var opts []cel.EnvOption
	for _, m := range t.Messages {
		rt, err := buildStruct(m)
		if err != nil {
			return nil, err
		}
		nt, err := celtypes.NewNativeType(rt,
			celtypes.NativeTypeAlias(strings.TrimPrefix(m.Name, ".")),
			celtypes.ParseStructTag("cel"))
		if err != nil {
			return nil, fmt.Errorf("env.types.messages[%q]: %w", m.Name, err)
		}
		opts = append(opts, cel.Types(nt))
	}
	if len(t.Enums) > 0 {
		return nil, fmt.Errorf("env.types.enums: native enums are not supported; declare enums via a descriptor or represent them as messages")
	}
	return opts, nil
}

// buildStruct constructs a Go struct type from a Message declaration. The Go
// field name is exported; the CEL-visible name is the lowercase config name
// via the cel struct tag.
func buildStruct(m Message) (reflect.Type, error) {
	if m.Name == "" {
		return nil, fmt.Errorf("env.types.messages: missing name")
	}
	fields := make([]reflect.StructField, 0, len(m.Fields))
	seen := map[string]string{}
	for _, f := range m.Fields {
		if f.Name == "" {
			return nil, fmt.Errorf("env.types.messages[%q]: field missing name", m.Name)
		}
		ft, err := goFieldType(f.Type)
		if err != nil {
			return nil, fmt.Errorf("env.types.messages[%q] field %q: %w", m.Name, f.Name, err)
		}
		goName, err := exportedName(f.Name)
		if err != nil {
			return nil, fmt.Errorf("env.types.messages[%q] field %q: %w", m.Name, f.Name, err)
		}
		if prev, dup := seen[goName]; dup {
			return nil, fmt.Errorf("env.types.messages[%q]: fields %q and %q both map to Go field %q", m.Name, prev, f.Name, goName)
		}
		seen[goName] = f.Name
		fields = append(fields, reflect.StructField{
			Name: goName,
			Type: ft,
			Tag:  reflect.StructTag(`cel:"` + f.Name + `"`),
		})
	}
	return reflect.StructOf(fields), nil
}

// exportedName returns a name suitable for an exported reflect.StructField
// (leading uppercase) derived from a CEL field name, or an error when the
// name cannot be turned into a valid exported Go identifier (e.g. one that
// starts with an underscore or otherwise would panic reflect.StructOf).
func exportedName(name string) (string, error) {
	r, size := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError {
		return "", fmt.Errorf("name %q is not valid UTF-8", name)
	}
	goName := string(unicode.ToUpper(r)) + name[size:]
	if !isExportedIdent(goName) {
		return "", fmt.Errorf("name %q does not map to a valid exported Go identifier", name)
	}
	return goName, nil
}

// isExportedIdent reports whether s is a valid exported Go identifier: its
// first rune is an uppercase letter and the rest are letters, digits or
// underscores.
func isExportedIdent(s string) bool {
	for i, r := range s {
		if i == 0 {
			if !unicode.IsUpper(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// goFieldType maps a CEL type expression to the Go field type used when
// synthesizing a native struct.
func goFieldType(t string) (reflect.Type, error) {
	switch t {
	case "int":
		return reflect.TypeOf(int64(0)), nil
	case "uint":
		return reflect.TypeOf(uint64(0)), nil
	case "bool":
		return reflect.TypeOf(false), nil
	case "string":
		return reflect.TypeOf(""), nil
	case "double":
		return reflect.TypeOf(float64(0)), nil
	case "bytes":
		return reflect.TypeOf([]byte{}), nil
	case "dyn":
		return reflect.TypeOf((*any)(nil)).Elem(), nil
	case "timestamp":
		return reflect.TypeOf(time.Time{}), nil
	case "duration":
		return reflect.TypeOf(time.Duration(0)), nil
	}
	if strings.HasPrefix(t, "list<") && strings.HasSuffix(t, ">") {
		elem, err := goFieldType(strings.TrimSuffix(strings.TrimPrefix(t, "list<"), ">"))
		if err != nil {
			return nil, err
		}
		return reflect.SliceOf(elem), nil
	}
	return nil, fmt.Errorf("unsupported field type %q", t)
}
