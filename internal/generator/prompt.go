package generator

import (
	"fmt"
	"strings"
)

// Request is everything the prompt builder needs for one behavior.
type Request struct {
	BehaviorPath      string // project-relative path to the behavior markdown
	BehaviorContent   string
	OutputPath        string // project-relative path the tests belong at
	Language          string
	Framework         string
	ExistingTests     string // current contents of OutputPath, empty if new
	ExtraInstructions string
}

// BuildPrompt renders the generation prompt.
//
// The prompt asks the harness to write the file itself, because every supported
// harness is an agent with file tools; that keeps generated code out of the
// stdout channel where agents also emit their own commentary. Generator.Generate
// still falls back to parsing stdout when a harness declines to write.
func BuildPrompt(r Request) string {
	var b strings.Builder

	b.WriteString("You are generating an automated test suite from a written product behavior specification.\n\n")

	b.WriteString("## Task\n\n")
	fmt.Fprintf(&b, "Read the behavior specification below and write %s tests for it using %s.\n",
		r.Language, r.Framework)
	fmt.Fprintf(&b, "Write the result to exactly this path, relative to the current working directory:\n\n    %s\n\n",
		r.OutputPath)
	b.WriteString("Create any parent directories the path needs. Write the file yourself using your file tools — do not print the test code as your reply.\n\n")

	if r.ExistingTests != "" {
		b.WriteString("## Existing tests at that path\n\n")
		b.WriteString("This file already exists, generated from an earlier version of the same specification. ")
		b.WriteString("Update it to match the specification as it now reads: change the cases the specification changed, ")
		b.WriteString("add cases it added, and remove cases it no longer describes. ")
		b.WriteString("Preserve unrelated hand-written helpers, fixtures and imports rather than rewriting the file from scratch.\n\n")
		b.WriteString(fence(r.ExistingTests))
		b.WriteString("\n\n")
	}

	b.WriteString("## Behavior specification\n\n")
	fmt.Fprintf(&b, "Source file: %s\n\n", r.BehaviorPath)
	b.WriteString(fence(r.BehaviorContent))
	b.WriteString("\n\n")

	b.WriteString("## Requirements\n\n")
	b.WriteString("- Cover every behavior the specification states, including the error and edge cases it calls out. One test per distinct asserted behavior.\n")
	b.WriteString("- Name each test after the behavior it verifies, so a failure names what broke.\n")
	b.WriteString("- Assert on the behavior the specification describes, not on incidental implementation detail.\n")
	b.WriteString("- Where the specification is silent on a detail you need, pick the reasonable interpretation and note it in a brief comment rather than inventing a requirement.\n")
	b.WriteString("- Match the conventions of the surrounding codebase: read a neighbouring test file first and follow its import style, fixture setup, naming and assertion library.\n")
	b.WriteString("- If the code under test exists in this repository, read it so the tests call the real API. Do not invent function signatures you have not verified.\n")
	b.WriteString("- The file must compile and be runnable by the project's normal test command.\n")
	b.WriteString("- Do not modify any file other than the target test file.\n")
	b.WriteString("- Do not run the test suite; katana runs it separately.\n")

	if strings.TrimSpace(r.ExtraInstructions) != "" {
		b.WriteString("\n## Additional project instructions\n\n")
		b.WriteString(strings.TrimSpace(r.ExtraInstructions))
		b.WriteString("\n")
	}

	b.WriteString("\nWhen the file is written, reply with one short line confirming the path. Nothing else.\n")

	return b.String()
}

// fence wraps content in a code fence long enough not to be terminated early by
// fences inside the content itself.
func fence(content string) string {
	ticks := "```"
	for strings.Contains(content, ticks) {
		ticks += "`"
	}
	return ticks + "\n" + strings.TrimRight(content, "\n") + "\n" + ticks
}
