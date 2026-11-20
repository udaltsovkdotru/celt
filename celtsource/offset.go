package celtsource

import "unicode/utf8"

func RuneOffsets(content string) []int {
	off := make([]int, 0, len(content))
	o := 0
	for o < len(content) {
		off = append(off, o)
		_, size := utf8.DecodeRuneInString(content[o:])
		o += size
	}
	return append(off, o)
}

func ByteOffset(off []int, runeOff int) int {
	if runeOff < 0 {
		return 0
	}
	if runeOff >= len(off) {
		return off[len(off)-1]
	}
	return off[runeOff]
}
