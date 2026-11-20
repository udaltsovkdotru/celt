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
	if strings.HasPrefix(s, "list<") && strings.HasSuffix(s, ">") {
		elem, err := parseType(s[len("list<") : len(s)-1])
		if err != nil {
			return nil, fmt.Errorf("list: %w", err)
		}
		return cel.ListType(elem), nil
	}
	if strings.HasPrefix(s, "map<") && strings.HasSuffix(s, ">") {
		inner := s[len("map<") : len(s)-1]
		key, val, err := splitTypePair(inner)
		if err != nil {
			return nil, fmt.Errorf("map: %w", err)
		}
		kt, err := parseType(key)
		if err != nil {
			return nil, fmt.Errorf("map key: %w", err)
		}
		vt, err := parseType(val)
		if err != nil {
			return nil, fmt.Errorf("map value: %w", err)
		}
		return cel.MapType(kt, vt), nil
	}
	if strings.HasPrefix(s, "optional<") && strings.HasSuffix(s, ">") {
		elem, err := parseType(s[len("optional<") : len(s)-1])
		if err != nil {
			return nil, fmt.Errorf("optional: %w", err)
		}
		return cel.OptionalType(elem), nil
	}
	// Object type name, e.g. "sec.foo.Bar" or ".Bar".
	return cel.ObjectType(strings.TrimPrefix(s, ".")), nil
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
// comma, honoring nested angle brackets.
func splitTypePair(s string) (key, val string, err error) {
	depth := 0
	for i := range len(s) {
		switch s[i] {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				key = strings.TrimSpace(s[:i])
				val = strings.TrimSpace(s[i+1:])
				if key == "" || val == "" {
					return "", "", fmt.Errorf("malformed pair %q", s)
				}
				return key, val, nil
			}
		}
	}
	return "", "", fmt.Errorf("no comma in %q", s)
}
