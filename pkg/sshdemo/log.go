package sshdemo

import (
	"strings"
	"unicode"
)

func safeLogValue(value string) string {
	const maxRunes = 128
	var result strings.Builder
	count := 0
	for _, r := range value {
		if count == maxRunes {
			break
		}
		if unicode.IsControl(r) {
			r = '�'
		}
		result.WriteRune(r)
		count++
	}
	return result.String()
}
