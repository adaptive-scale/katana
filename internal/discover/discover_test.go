package discover

import "testing"

func TestSkipReason(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   string
	}{
		{"plain", "SKIP: only constants here", "only constants here"},
		{"after commentary", "I read the files.\n\nSKIP: generated protobuf code\n", "generated protobuf code"},
		{"decorated by the agent", "**SKIP: no rules of its own**", "no rules of its own"},
		{"lower case", "skip: nothing to say", "nothing to say"},
		{"no reason given", "SKIP:", "no behavior to specify"},
		// The word inside a sentence is commentary, not the reply katana asked
		// for: the harness was told to send the marker on a line of its own.
		{"mentioned in passing", "I had to SKIP: two files while writing the spec.", ""},
		{"nothing to skip", "Wrote behaviors/app.md", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := skipReason(c.stdout); got != c.want {
				t.Errorf("skipReason(%q) = %q, want %q", c.stdout, got, c.want)
			}
		})
	}
}

func TestExtractMarkdownTakesTheSpecificationOutOfAReply(t *testing.T) {
	stdout := "I could not write the file, so here it is:\n\n" +
		"```markdown\n# Billing\n\n## Charging a card\n\n- A charge below the minimum is rejected.\n- A declined card leaves the order unpaid.\n```\n"
	got := extractMarkdown(stdout)
	if got == "" {
		t.Fatal("a fenced specification should be recovered")
	}
	if got[0] != '#' {
		t.Errorf("recovered body should start at the heading, got %q", got)
	}
}

func TestExtractMarkdownRefusesAgentChatter(t *testing.T) {
	for _, stdout := range []string{
		"",
		"Done — I wrote the behavior file to behaviors/app.md.",
		"I read the files but was unsure what to write.\n\nLet me know how you would like to proceed.",
		// A heading with a single bullet is a reply about the work, not a
		// specification of it.
		"# Summary\n\n- wrote one file\n",
	} {
		if got := extractMarkdown(stdout); got != "" {
			t.Errorf("extractMarkdown(%q) = %q, want empty", stdout, got)
		}
	}
}

// Unfenced output is accepted when it is unmistakably a specification, since
// some harnesses print the file without wrapping it.
func TestExtractMarkdownAcceptsUnfencedSpecification(t *testing.T) {
	stdout := "# Cart\n\n## Checkout\n\n- An empty cart cannot be checked out.\n- A discount never takes the total below zero.\n"
	if got := extractMarkdown(stdout); got != stdout {
		t.Errorf("extractMarkdown() = %q, want the specification verbatim", got)
	}
}
