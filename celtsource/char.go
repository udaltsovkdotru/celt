package celtsource

func IsSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func IsIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func IsIdentStart(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func IsIdentChar(b byte) bool {
	return b == '.' || IsIdentByte(b)
}

func SkipString(content string, i int) int {
	quote := content[i]
	// In a raw string (r/R prefix) a backslash is literal, not an escape, so it
	// cannot mask the closing quote. Handle it separately before the
	// escape-aware logic below, which would otherwise skip past the delimiter.
	if rawStringAt(content, i) {
		return skipRawString(content, i)
	}
	// A triple-quoted string ("...", '...') spans until its closing triple
	// quote, including embedded single quotes; skipping it as a single-quote
	// literal would resume inside the content.
	if i+2 < len(content) && content[i+1] == quote && content[i+2] == quote {
		i += 3
		for i+2 < len(content) {
			// An escaped quote ("\"" ) is part of the content, not a delimiter;
			// skip it so a sequence like `\"""` isn't mistaken for the close.
			if content[i] == '\\' {
				i += 2
				continue
			}
			if content[i] == quote && content[i+1] == quote && content[i+2] == quote {
				return i + 3
			}
			i++
		}
		return len(content)
	}
	i++
	for i < len(content) {
		if content[i] == '\\' {
			i += 2
			continue
		}
		if content[i] == quote {
			return i + 1
		}
		i++
	}
	return i
}

// rawStringAt reports whether the quote at i opens a raw string literal: a
// single- or two-character bytes/raw prefix containing an r/R immediately
// before the quote, not appended to a longer identifier.
func rawStringAt(content string, i int) bool {
	start := i
	for start > 0 && isRawPrefixByte(content[start-1]) {
		start--
	}
	prefix := content[start:i]
	if prefix == "" || len(prefix) > 2 {
		return false
	}
	hasRaw := false
	for k := range prefix {
		if prefix[k] == 'r' || prefix[k] == 'R' {
			hasRaw = true
			break
		}
	}
	if !hasRaw {
		return false
	}
	// A prefix glued to a longer identifier (e.g. `myr"..."`) is not a raw
	// string; only a standalone prefix bounded by a non-identifier char is.
	if start > 0 && IsIdentByte(content[start-1]) {
		return false
	}
	return true
}

func isRawPrefixByte(c byte) bool {
	return c == 'r' || c == 'R' || c == 'b' || c == 'B'
}

// skipRawString skips a raw string literal starting at the opening quote i. A
// backslash is literal; an embedded quote is escaped by doubling it ("").
func skipRawString(content string, i int) int {
	quote := content[i]
	if i+2 < len(content) && content[i+1] == quote && content[i+2] == quote {
		i += 3
		for i+2 < len(content) {
			if content[i] == quote && content[i+1] == quote && content[i+2] == quote {
				return i + 3
			}
			i++
		}
		return len(content)
	}
	i++
	for i < len(content) {
		if content[i] == quote {
			if i+1 < len(content) && content[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}
