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
