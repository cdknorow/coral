package board

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// Warnings about @mentions that name nobody on the board (#4583 rule 5). A post is never refused for one: the warning comes back with
// the post, naming the closest member ("no subscriber named 'DO Expert'; did you mean 'Digital Ocean Expert'?").

// knownMentionWords are the tags that name no member and are still tags (mentionTerms).
var knownMentionWords = []string{"all", "notify-all", "notify_all", "notifyall", "operator"}

// MentionWarnings returns one warning for each @mention in content that matches no subscriber's name or role, no spaceless form of
// one and no special tag. A mention at the start of the message (the run that "coral-board post --to" writes) is warned about with or
// without a close name; one in the middle of the text only when a member's name is close to it (an "@param" or "@2x" in a message is
// not a mistake). At most five warnings.
func (s *Store) MentionWarnings(ctx context.Context, project, content string) []string {
	if !strings.Contains(content, "@") {
		return nil
	}
	subs, err := s.ListSubscribers(ctx, project)
	if err != nil || len(subs) == 0 {
		return nil // a board with no members names nobody: nothing to compare with
	}
	var names []string
	for _, sub := range subs {
		names = append(names, sub.SubscriberID)
		if t := strings.TrimSpace(sub.JobTitle); t != "" {
			names = append(names, t)
		}
	}
	return mentionWarnings(content, names)
}

func mentionWarnings(content string, names []string) []string {
	known := map[string]bool{}
	for _, w := range knownMentionWords {
		known[w] = true
	}
	var display []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		l := strings.ToLower(n)
		if l == "" {
			continue
		}
		known[l] = true
		known[strings.ReplaceAll(l, " ", "")] = true
		if !seen[l] {
			seen[l] = true
			display = append(display, n)
		}
	}
	var out []string
	warned := map[string]bool{}
	lead := true // still inside the run of mentions at the start of the message
	for i := 0; i < len(content); {
		c := content[i]
		if c != '@' {
			if lead && c != ' ' && c != ',' {
				lead = false
			}
			i++
			continue
		}
		if i > 0 && isMentionWordByte(content[i-1]) {
			lead = false // an address (user@host), not a mention
			i++
			continue
		}
		rest := content[i+1:]
		if n := knownLen(strings.ToLower(rest), known); n > 0 {
			i += 1 + n
			continue
		}
		words, ends := mentionWords(rest, 4)
		if len(words) == 0 {
			lead = false
			i++
			continue
		}
		bestName, bestWords, bestDist := "", 0, 1<<30
		for k := 1; k <= len(words); k++ {
			cand := strings.ToLower(strings.Join(words[:k], " "))
			for _, d := range display {
				dist := editDistance(cand, strings.ToLower(d))
				if abbreviates(words[:k], d) {
					dist = 0 // "DO Expert" is "Digital Ocean Expert"
				}
				if dist < bestDist || (dist == bestDist && k > bestWords && dist == 0) || (dist == bestDist && k < bestWords) {
					bestName, bestWords, bestDist = d, k, dist
				}
			}
		}
		used := 1
		typed, suggest := words[0], ""
		if bestName != "" && bestDist <= max(1, len(bestName)/3) {
			used, typed, suggest = bestWords, strings.Join(words[:bestWords], " "), bestName
		}
		i += 1 + ends[used-1]
		if suggest == "" && !lead {
			continue // a mention in the middle of the text that is close to no member: not a mistake we can name
		}
		if key := strings.ToLower(typed); !warned[key] {
			warned[key] = true
			w := fmt.Sprintf("no subscriber named '%s'", typed)
			if suggest != "" {
				w += fmt.Sprintf("; did you mean '%s'?", suggest)
			}
			out = append(out, w)
			if len(out) == 5 {
				break
			}
		}
	}
	return out
}

func isMentionWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// knownLen is the length of the longest known name rest (lower-cased, after an "@") starts with, ending at a word boundary; 0 when
// there is none.
func knownLen(rest string, known map[string]bool) int {
	for l := min(len(rest), 64); l >= 1; l-- {
		if l < len(rest) && isMentionWordByte(rest[l]) && isMentionWordByte(rest[l-1]) {
			continue
		}
		if known[strings.TrimRight(rest[:l], " ")] {
			return len(strings.TrimRight(rest[:l], " "))
		}
	}
	return 0
}

// mentionWords are the first n words after an "@", up to a newline or a punctuation mark that ends a name, and where each ends in rest.
func mentionWords(rest string, n int) (words []string, ends []int) {
	end := len(rest)
	for i, r := range rest {
		if r == '\n' || r == '\r' || r == ',' || r == ';' || r == ':' || r == '!' || r == '?' || r == ')' || r == '(' || r == '"' || r == '@' || r == '`' {
			end = i
			break
		}
	}
	pos := 0
	for pos < end {
		for pos < end && (rest[pos] == ' ' || rest[pos] == '\t') {
			pos++
		}
		start := pos
		for pos < end && rest[pos] != ' ' && rest[pos] != '\t' {
			pos++
		}
		w := strings.TrimRight(rest[start:pos], ".'")
		if w == "" {
			break
		}
		words = append(words, w)
		ends = append(ends, start+len(w))
		if len(words) == n {
			break
		}
	}
	return words, ends
}

// abbreviates: typed (words after an "@") is a shortened form of name: each typed word is the start of the next word of the name, or
// the initials of the next few ("DO Expert" for "Digital Ocean Expert"), and together they use the whole name.
func abbreviates(typed []string, name string) bool {
	nw := strings.Fields(strings.ToLower(name))
	if len(nw) < 2 || len(typed) == 0 {
		return false
	}
	j := 0
	for _, t := range typed {
		t = strings.ToLower(t)
		switch {
		case j < len(nw) && len(t) >= 2 && strings.HasPrefix(nw[j], t):
			j++
		case len(t) >= 2 && j+len(t) <= len(nw) && initials(nw[j:j+len(t)]) == t:
			j += len(t)
		default:
			return false
		}
	}
	return j == len(nw)
}

func initials(words []string) string {
	var b strings.Builder
	for _, w := range words {
		b.WriteByte(w[0])
	}
	return b.String()
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if unicode.ToLower(ra[i-1]) == unicode.ToLower(rb[j-1]) {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
