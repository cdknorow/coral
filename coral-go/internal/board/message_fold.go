package board

import "strings"

// textHasFold reports whether strings.ToLower(s) contains lowerNeedle (already lower-case), without allocating the lower-cased copy
// a search would otherwise build for each of its messages. An ASCII needle against ASCII text is compared byte by byte; any other
// text, or a needle with a non-ASCII byte, takes the ToLower path, which folds the characters ASCII folding would miss.
func textHasFold(s, lowerNeedle string) bool {
	n := len(lowerNeedle)
	if n == 0 {
		return true
	}
	for i := 0; i < n; i++ {
		if lowerNeedle[i] >= 0x80 {
			return strings.Contains(strings.ToLower(s), lowerNeedle)
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return strings.Contains(strings.ToLower(s), lowerNeedle)
		}
	}
	first := lowerNeedle[0]
	upper := first
	if first >= 'a' && first <= 'z' {
		upper = first - 'a' + 'A'
	}
	for i := 0; i+n <= len(s); i++ {
		if c := s[i]; c != first && c != upper {
			continue
		}
		j := 1
		for ; j < n; j++ {
			c := s[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != lowerNeedle[j] {
				break
			}
		}
		if j == n {
			return true
		}
	}
	return false
}
