package config

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/cel"
)

// parseType converts a CEL type expression string into a *cel.Type. Supported
// forms are primitives, object type names, and list<T>/map<K,V>/optional<T>.
func parseType(s string) (*cel.Type, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty type")
	}
	if prim, ok := primitiveType(s); ok {
		return prim, nil
	}
	if t, handled, err := parseGenericType(s); handled {
		return t, err
	}
	return parseObjectType(s)
}

// parseGenericType parses a list<T>, map<K,V> or optional<T> type expression.
// handled reports whether s had a generic form (either parsed, or rejected as
// malformed); when false, s is not a generic type at all.
func parseGenericType(s string) (t *cel.Type, handled bool, err error) {
	if strings.HasPrefix(s, "list<") && strings.HasSuffix(s, ">") {
		elem, err := parseType(s[len("list<") : len(s)-1])
		if err != nil {
			return nil, true, fmt.Errorf("list: %w", err)
		}
		return cel.ListType(elem), true, nil
	}
	if strings.HasPrefix(s, "map<") && strings.HasSuffix(s, ">") {
		inner := s[len("map<") : len(s)-1]
		key, val, err := splitTypePair(inner)
		if err != nil {
			return nil, true, fmt.Errorf("map: %w", err)
		}
		kt, err := parseType(key)
		if err != nil {
			return nil, true, fmt.Errorf("map key: %w", err)
		}
		vt, err := parseType(val)
		if err != nil {
			return nil, true, fmt.Errorf("map value: %w", err)
		}
		return cel.MapType(kt, vt), true, nil
	}
	if strings.HasPrefix(s, "optional<") && strings.HasSuffix(s, ">") {
		elem, err := parseType(s[len("optional<") : len(s)-1])
		if err != nil {
			return nil, true, fmt.Errorf("optional: %w", err)
		}
		return cel.OptionalType(elem), true, nil
	}
	// A type that opens a generic parameter but lacks its closing bracket is
	// malformed, not an object type name (e.g. "list<int" must not become an
	// object type).
	if strings.HasPrefix(s, "list<") || strings.HasPrefix(s, "map<") || strings.HasPrefix(s, "optional<") {
		return nil, true, fmt.Errorf("unclosed type expression %q", s)
	}
	return nil, false, nil
}

// parseObjectType validates and constructs a CEL object type from a (possibly
// leading-dot) qualified name. It rejects malformed names: empty or dotted-only
// names, empty components ("pkg..Person"), stray delimiters ("int>"), and
// non-identifier characters. The leading dot, if present, is normalized away
// exactly as registration does, so aliases of the same name are comparable.
func parseObjectType(name string) (*cel.Type, error) {
	normalized := strings.TrimPrefix(name, ".")
	if !isQualifiedIdent(normalized) {
		return nil, fmt.Errorf("malformed type name %q", name)
	}
	return cel.ObjectType(normalized), nil
}

// isQualifiedIdent reports whether s is a valid dotted CEL identifier: one or
// more identifier components separated by single dots, each starting with a
// letter or underscore and continuing with letters, digits or underscores.
func isQualifiedIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if !isIdent(part) {
			return false
		}
	}
	return true
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := range s {
		c := s[i]
		switch {
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

func primitiveType(s string) (*cel.Type, bool) {
	switch s {
	case "bool":
		return cel.BoolType, true
	case "int":
		return cel.IntType, true
	case "uint":
		return cel.UintType, true
	case "double":
		return cel.DoubleType, true
	case "string":
		return cel.StringType, true
	case "bytes":
		return cel.BytesType, true
	case "dyn":
		return cel.DynType, true
	case "null":
		return cel.NullType, true
	case "timestamp":
		return cel.TimestampType, true
	case "duration":
		return cel.DurationType, true
	case "type":
		return cel.TypeType, true
	}
	return nil, false
}

// splitTypePair splits a map<K,V> body into key and value at the top-level
// comma, honoring nested angle brackets. A map takes exactly two arguments, so
// any additional top-level comma is malformed.
func splitTypePair(s string) (key, val string, err error) {
	depth := 0
	commas := make([]int, 0, 1)
	for i := range len(s) {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				commas = append(commas, i)
			}
		}
	}
	if len(commas) != 1 {
		return "", "", fmt.Errorf("malformed pair %q", s)
	}
	sep := commas[0]
	key = strings.TrimSpace(s[:sep])
	val = strings.TrimSpace(s[sep+1:])
	if key == "" || val == "" {
		return "", "", fmt.Errorf("malformed pair %q", s)
	}
	return key, val, nil
}
