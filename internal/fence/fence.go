// Package fence reads content out of markdown code fences and puts it back in.
//
// Agents wrap the files they print in fences and surround them with prose, and
// katana quotes files back to agents the same way. Both directions have to cope
// with content that contains fences of its own, so the rules live in one place.
package fence

import "strings"

// Blocks returns the contents of every ``` fenced block in s, in order.
func Blocks(s string) []string {
	var blocks []string
	lines := strings.Split(s, "\n")
	var cur []string
	var fence string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fence == "" {
			if strings.HasPrefix(trimmed, "```") {
				fence = strings.Repeat("`", countLeading(trimmed, '`'))
				cur = nil
			}
			continue
		}
		// Inside a block: a line that is only backticks of at least the opening
		// length closes it.
		if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, "`") == "" {
			blocks = append(blocks, strings.Join(cur, "\n"))
			fence = ""
			cur = nil
			continue
		}
		cur = append(cur, line)
	}
	// An unterminated block still holds usable content.
	if fence != "" && len(cur) > 0 {
		blocks = append(blocks, strings.Join(cur, "\n"))
	}
	return blocks
}

// Largest returns the longest fenced block in s, or "" when there is none. It
// is how katana picks the file body out of a reply that fenced several things:
// the file is the big one, the snippets around it are commentary.
func Largest(s string) string {
	blocks := Blocks(s)
	if len(blocks) == 0 {
		return ""
	}
	best := blocks[0]
	for _, b := range blocks[1:] {
		if len(b) > len(best) {
			best = b
		}
	}
	return best
}

// Wrap encloses content in a fence long enough not to be terminated early by a
// fence inside the content itself.
func Wrap(content string) string {
	ticks := "```"
	for strings.Contains(content, ticks) {
		ticks += "`"
	}
	return ticks + "\n" + strings.TrimRight(content, "\n") + "\n" + ticks
}

func countLeading(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}
