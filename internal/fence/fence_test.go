package fence

import (
	"strings"
	"testing"
)

func TestWrapEscapesNestedFences(t *testing.T) {
	// A behavior spec containing a code fence must not terminate the wrapper
	// early, or the harness sees a truncated specification.
	content := "Example:\n```go\nfoo()\n```\nEnd."
	wrapped := Wrap(content)
	if wrapped[:4] != "````" {
		t.Errorf("wrapper should outgrow the nested fence, got:\n%s", wrapped)
	}
	if Blocks(wrapped)[0] != content {
		t.Errorf("wrapped content should come back whole, got:\n%s", Blocks(wrapped)[0])
	}
}

func TestBlocks(t *testing.T) {
	stdout := "Here you go:\n\n```go\nfunc A() {}\n```\n\nand a note:\n\n```\nsecond\n```\n"
	got := Blocks(stdout)
	if len(got) != 2 {
		t.Fatalf("got %d blocks, want 2: %q", len(got), got)
	}
	if got[0] != "func A() {}" || got[1] != "second" {
		t.Errorf("blocks = %q", got)
	}
}

func TestLargestPrefersTheFileOverCommentary(t *testing.T) {
	stdout := "```sh\nrun me\n```\n\n```go\npackage tests\n\nfunc TestA(t *testing.T) {}\n```\n"
	if got := Largest(stdout); got != "package tests\n\nfunc TestA(t *testing.T) {}" {
		t.Errorf("Largest() = %q", got)
	}
	if got := Largest("no fences here"); got != "" {
		t.Errorf("Largest() with no fence = %q, want empty", got)
	}
}

// An agent that stops mid-reply still leaves the file it was printing.
func TestBlocksKeepsUnterminatedBlock(t *testing.T) {
	got := Blocks("```go\npackage tests\nfunc TestA() {}\n")
	if len(got) != 1 || strings.TrimSpace(got[0]) != "package tests\nfunc TestA() {}" {
		t.Errorf("Blocks() = %q", got)
	}
}
