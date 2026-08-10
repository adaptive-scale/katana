package discover

import (
	"fmt"
	"strings"

	"github.com/adaptive-scale/katana/internal/fence"
)

// Request is everything the prompt builder needs for one unit.
type Request struct {
	Unit              Unit
	Language          string
	ExistingBehavior  string // current contents of Unit.Output, empty if new
	ExtraInstructions string
}

// SkipMarker is the reply a harness sends instead of writing a file when the
// source it read states no product behavior worth specifying.
const SkipMarker = "SKIP:"

// BuildPrompt renders the discovery prompt.
//
// It asks for the same thing a person writing a behavior file by hand would
// produce, and says so in the format the rest of katana reads: a title, one
// section per group of related behavior, and bullets that each state one
// checkable fact. The harness is asked to write the file itself for the same
// reason generation is — every supported harness is an agent with file tools,
// and that keeps the specification out of the stdout channel where the agent
// also puts its own commentary.
func BuildPrompt(r Request) string {
	var b strings.Builder

	b.WriteString("You are documenting the behavior an existing codebase already implements, as a plain-language specification.\n\n")

	b.WriteString("## Task\n\n")
	fmt.Fprintf(&b, "Read the %s source files listed below and describe what they do, from the outside.\n", r.Language)
	fmt.Fprintf(&b, "Write the result to exactly this path, relative to the current working directory:\n\n    %s\n\n", r.Unit.Output)
	b.WriteString("Create any parent directories the path needs. Write the file yourself using your file tools — do not print the specification as your reply.\n\n")
	b.WriteString("If, and only if, writing that file fails — no file tool is available, or the write is denied by a permission check — do not stop and do not ask for access. ")
	b.WriteString("Print the complete file contents as your entire reply, in a single fenced code block, with no prose before or after it. That output is saved to the path above verbatim.\n\n")

	fmt.Fprintf(&b, "## Source files (%s)\n\n", r.Unit.Name)
	for _, f := range r.Unit.Files {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	b.WriteString("\nRead every one of them before writing. They are the only source of what you write.\n\n")

	if strings.TrimSpace(r.ExistingBehavior) != "" {
		b.WriteString("## Existing specification at that path\n\n")
		b.WriteString("A specification is already written for these files. Update it to match the code as it now stands: ")
		b.WriteString("correct statements the code contradicts, add behavior it has gained, remove behavior it no longer has. ")
		b.WriteString("Keep the wording, ordering and structure of everything still true — this file may have been edited by hand, and those edits are the point.\n\n")
		b.WriteString(fence.Wrap(r.ExistingBehavior))
		b.WriteString("\n\n")
	}

	b.WriteString("## Format\n\n")
	b.WriteString("Markdown, in exactly this shape:\n\n")
	b.WriteString(fence.Wrap(formatExample))
	b.WriteString("\n\n")

	b.WriteString("## Requirements\n\n")
	b.WriteString("- Describe observable behavior: what goes in, what comes out, what changes, what is rejected. Never how it is implemented.\n")
	b.WriteString("- One bullet per fact that could be checked on its own. A bullet that needs the word \"and\" twice is two bullets.\n")
	b.WriteString("- State the rules the code actually enforces — defaults it applies, values it validates, limits it imposes, errors it returns and what triggers each one.\n")
	b.WriteString("- Cover the edge cases the code handles: empty input, missing values, duplicates, boundaries, failure paths. These are the behaviors most worth having tests for.\n")
	b.WriteString("- Write for someone who cannot see the code. Name the concepts the code works with, not its private helpers, and quote real error messages and literal values where the behavior depends on them.\n")
	b.WriteString("- Every statement must be something you read in these files. Do not describe intended, planned or assumed behavior, and do not soften a bullet into something unverifiable.\n")
	b.WriteString("- Where the code's behavior is genuinely unclear, say what it does in one bullet rather than guessing why; a wrong guess becomes a wrong test.\n")
	b.WriteString("- Leave out pure plumbing — constructors, getters, string formatting with no rules of its own — unless the plumbing is the behavior.\n")
	b.WriteString("- Do not include code, code fences, function signatures or file paths in the specification.\n")
	b.WriteString("- Do not modify any file other than the one named above. Do not write tests; katana generates those from what you write.\n")

	if strings.TrimSpace(r.ExtraInstructions) != "" {
		b.WriteString("\n## Additional project instructions\n\n")
		b.WriteString(strings.TrimSpace(r.ExtraInstructions))
		b.WriteString("\n")
	}

	b.WriteString("\n## If there is nothing to specify\n\n")
	b.WriteString("Some files hold no product behavior: generated code, plain data declarations, constants, a package that only wires other packages together. ")
	fmt.Fprintf(&b, "For those, do not write the file. Reply with one line, and nothing else:\n\n    %s <the reason, in a few words>\n\n", SkipMarker)

	b.WriteString("When the file is written, reply with one short line confirming the path. Nothing else. ")
	b.WriteString("If you could not write it, reply with the fenced file contents as described above.\n")

	return b.String()
}

// formatExample is the shape every behavior file katana reads has: a title, a
// section per group of related behavior, and bullets that each state one thing.
const formatExample = `# What this part of the product does

One or two sentences of context: what this is for, and who or what uses it.

## A group of related behavior

- A single statement of observable behavior, phrased so it can be checked.
- Another, covering a different case of the same group.

## Another group

- What happens when the input is missing or invalid, and what is reported.
- What the boundary case does, stated exactly.
`
