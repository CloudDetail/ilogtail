// exposed some extra metric

package logparser

import "strings"

func IsContainsTimestamp(line string) bool {
	return containsTimestamp(line)
}

// IsFirstLine 从固定格式判断是否为首行
func IsFirstLine(l string) bool {
	if l == "" ||
		l == "}" ||
		strings.HasPrefix(l, "\t") ||
		strings.HasPrefix(l, "  ") {
		return false
	}

	if strings.HasPrefix(l, "Caused by: ") {
		return false
	}

	if strings.HasPrefix(l, "for call at") {
		return false
	}

	return true
}
