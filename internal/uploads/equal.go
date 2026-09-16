package uploads

import "strings"

// equalFold MIME 比较不区分大小写。
func equalFold(a, b string) bool {
	return strings.EqualFold(a, b)
}
