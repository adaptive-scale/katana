// This file covers behaviors/internal/fence.md: pulling file contents out of
// the fenced blocks in an agent's reply, picking the file body out of a reply
// that fenced several things, and wrapping content in a fence when katana
// quotes a file back to an agent.
//
// Every assertion goes through the fence package's exported API — Blocks,
// Largest and Wrap — since those are the three directions the specification
// describes.

package internal

import (
	"strings"
	"testing"

	"github.com/adaptive-scale/katana/internal/fence"
)

// assertBlocks fails the test unless Blocks(in) returns exactly want. Lengths
// are compared first so a miscount reports before an index panic; a nil and an
// empty result both count as "no blocks", which is all the specification says.
func assertBlocks(t *testing.T, in string, want []string) {
	t.Helper()
	got := fence.Blocks(in)
	if len(got) != len(want) {
		t.Fatalf("Blocks(%q) = %d blocks %q, want %d %q", in, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Blocks(%q)[%d] = %q, want %q", in, i, got[i], want[i])
		}
	}
}

// assertOneBlock fails the test unless in holds exactly one fenced block whose
// content is want, and returns that content for further assertions.
func assertOneBlock(t *testing.T, in, want string) string {
	t.Helper()
	got := fence.Blocks(in)
	if len(got) != 1 {
		t.Fatalf("Blocks(%q) = %d blocks %q, want 1", in, len(got), got)
	}
	if got[0] != want {
		t.Errorf("Blocks(%q)[0] = %q, want %q", in, got[0], want)
	}
	return got[0]
}

// lines splits a wrapped result so the fence lines can be inspected on their own.
func lines(s string) []string { return strings.Split(s, "\n") }

// --- Collecting fenced blocks from text ------------------------------------

func TestBlocksReturnsEveryFencedBlockInTheOrderTheyAppear(t *testing.T) {
	in := "Here you go:\n\n```go\nfunc A() {}\n```\n\nand a note:\n\n```\nsecond\n```\n"

	assertBlocks(t, in, []string{"func A() {}", "second"})
}

func TestABlockOpensAtALineBeginningWithThreeOrMoreBackticks(t *testing.T) {
	for _, c := range []struct{ name, in string }{
		{"three", "```\nbody\n```"},
		{"four", "````\nbody\n````"},
		{"six", "``````\nbody\n``````"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertOneBlock(t, c.in, "body")
		})
	}
}

func TestAnOpeningFenceIsRecognisedIgnoringSurroundingWhitespace(t *testing.T) {
	assertOneBlock(t, "  ```go  \nbody\n```", "body")
}

func TestTheOpeningFenceLineIsNotPartOfTheContent(t *testing.T) {
	got := assertOneBlock(t, "```\nbody\n```", "body")
	if strings.Contains(got, "`") {
		t.Errorf("content = %q, want no fence line in it", got)
	}
}

func TestInfoTextOnTheOpeningLineIsDiscarded(t *testing.T) {
	for _, c := range []struct{ name, open string }{
		{"language tag", "```go"},
		{"tag with attributes", "```go title=example.go"},
		{"arbitrary info text", "``` anything at all"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertOneBlock(t, c.open+"\nbody\n```", "body")
		})
	}
}

func TestTextOutsideAFencedBlockIsNotReturned(t *testing.T) {
	assertOneBlock(t, "prose before\n\n```\nbody\n```\n\nprose after", "body")
}

func TestTextWithNoFencedBlockReturnsNoBlocks(t *testing.T) {
	assertBlocks(t, "no fences here\njust prose about ` and `` backticks", nil)
}

func TestEmptyTextReturnsNoBlocks(t *testing.T) {
	assertBlocks(t, "", nil)
}

func TestContentLinesKeepTheirOriginalLeadingAndTrailingWhitespace(t *testing.T) {
	in := "```go\n    indented\ntrailing   \n```"

	assertOneBlock(t, in, "    indented\ntrailing   ")
}

func TestASingleLineBlockHasNoTrailingNewline(t *testing.T) {
	got := assertOneBlock(t, "```\nonly line\n```\n", "only line")
	if strings.HasSuffix(got, "\n") {
		t.Errorf("content = %q, want no trailing newline of its own", got)
	}
}

// --- Closing a block -------------------------------------------------------

func TestABlockClosesAtALineOfBackticksAsLongAsTheOpening(t *testing.T) {
	for _, c := range []struct{ name, in string }{
		{"three", "```\nbody\n```\nafter"},
		{"four", "````\nbody\n````\nafter"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertOneBlock(t, c.in, "body")
		})
	}
}

func TestAClosingFenceIsRecognisedIgnoringSurroundingWhitespace(t *testing.T) {
	assertOneBlock(t, "```\nbody\n   ```   \nafter", "body")
}

func TestTheClosingFenceLineIsNotPartOfTheContent(t *testing.T) {
	got := assertOneBlock(t, "```\nbody\n```", "body")
	if strings.Contains(got, "`") {
		t.Errorf("content = %q, want no closing fence in it", got)
	}
}

func TestALongerClosingFenceStillClosesTheBlock(t *testing.T) {
	assertOneBlock(t, "```\nbody\n`````\nafter", "body")
}

func TestABacktickLineShorterThanTheOpeningFenceIsKeptAsContent(t *testing.T) {
	// Opened with four, so the three-backtick line is content, not a close.
	assertOneBlock(t, "````\n```\nkept\n````", "```\nkept")
}

func TestALineWithTextAfterItsBackticksIsKeptAsContent(t *testing.T) {
	// A nested opening fence carrying a language tag: enough backticks to close,
	// but it is not backticks alone.
	assertOneBlock(t, "```\n```go\nnested\n```", "```go\nnested")
}

func TestScanningContinuesAfterABlockCloses(t *testing.T) {
	in := "```\nfirst\n```\nprose in between\n```\nsecond\n```"

	assertBlocks(t, in, []string{"first", "second"})
}

func TestAnEmptyBlockIsReportedAsABlockWithEmptyContent(t *testing.T) {
	in := "```\n```\n```\nsecond\n```"

	assertBlocks(t, in, []string{"", "second"})
}

// --- Blocks that are never closed -----------------------------------------

func TestAnUnclosedBlockCarriesEveryLineToTheEndOfTheText(t *testing.T) {
	// The fixture ends without a final newline: the specification does not say
	// whether a trailing newline contributes a last, empty content line.
	in := "```go\npackage tests\nfunc TestA() {}"

	assertOneBlock(t, in, "package tests\nfunc TestA() {}")
}

func TestABlockOpenedOnTheVeryLastLineIsDropped(t *testing.T) {
	// As above, no trailing newline, so there is genuinely nothing after the
	// opening fence.
	assertBlocks(t, "prose\n```go", nil)
}

// --- Picking the file body out of a reply ---------------------------------

func TestLargestTakesTheLongestBlockAsTheFile(t *testing.T) {
	in := "```sh\nrun me\n```\n\n```go\npackage tests\n\nfunc TestA(t *testing.T) {}\n```\n"

	want := "package tests\n\nfunc TestA(t *testing.T) {}"
	if got := fence.Largest(in); got != want {
		t.Errorf("Largest() = %q, want the file %q rather than the commentary", got, want)
	}
}

func TestLargestOfTextWithNoFencedBlockIsEmpty(t *testing.T) {
	if got := fence.Largest("no fences here"); got != "" {
		t.Errorf("Largest() = %q, want the empty string", got)
	}
}

func TestLargestComparesTheNumberOfCharactersNotLines(t *testing.T) {
	// The first block has more lines but fewer characters. Both fixtures are
	// ASCII, where character and byte counts agree; the specification does not
	// say which applies to non-ASCII content.
	in := "```\na\nb\nc\nd\ne\n```\n```\n0123456789abcdef\n```"

	if got := fence.Largest(in); got != "0123456789abcdef" {
		t.Errorf("Largest() = %q, want the block with the most characters", got)
	}
}

func TestLargestKeepsTheFirstOfTwoEqualLengthBlocks(t *testing.T) {
	in := "```\naaa\n```\n```\nbbb\n```"

	if got := fence.Largest(in); got != "aaa" {
		t.Errorf("Largest() = %q, want the earlier block %q", got, "aaa")
	}
}

func TestLargestOfASingleEmptyBlockIsThatEmptyContent(t *testing.T) {
	// The result is the same as for text with no fence at all; the
	// specification asks only that the single block's content is what comes back.
	if got := fence.Largest("```\n```"); got != "" {
		t.Errorf("Largest() = %q, want the one block's empty content", got)
	}
}

// --- Wrapping content in a fence ------------------------------------------

func TestWrapPutsTheFenceLinesOnTheirOwnLines(t *testing.T) {
	got := fence.Wrap("body")

	if want := "```\nbody\n```"; got != want {
		t.Errorf("Wrap(\"body\") = %q, want %q", got, want)
	}
	if n := len(lines(got)); n != 3 {
		t.Errorf("Wrap(\"body\") spans %d lines, want an opening fence, the content and a closing fence", n)
	}
}

func TestWrapUsesThreeBackticksWhenTheContentHasNoRunOfThree(t *testing.T) {
	got := fence.Wrap("one ``two`` three")

	if open := lines(got)[0]; open != "```" {
		t.Errorf("opening fence = %q, want ```", open)
	}
}

func TestWrapLengthensTheFenceUntilItIsAbsentFromTheContent(t *testing.T) {
	for _, c := range []struct{ name, content, wantFence string }{
		{"three backtick run", "before\n```\nafter", "````"},
		{"four backtick run", "before\n````\nafter", "`````"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := fence.Wrap(c.content)

			open := lines(got)[0]
			if open != c.wantFence {
				t.Errorf("opening fence = %q, want %q", open, c.wantFence)
			}
			if strings.Contains(c.content, open) {
				t.Errorf("fence %q still appears in the content %q, so the block would end early", open, c.content)
			}
		})
	}
}

func TestWrapLengthensForABacktickRunInTheMiddleOfALine(t *testing.T) {
	content := "a paragraph mentioning ```` inline"

	got := fence.Wrap(content)

	if open := lines(got)[0]; open != "`````" {
		t.Errorf("opening fence = %q, want ````` for a run that is not at a line start", open)
	}
}

func TestWrapDropsTrailingNewlinesFromTheContent(t *testing.T) {
	got := fence.Wrap("body\n\n\n")

	if want := "```\nbody\n```"; got != want {
		t.Errorf("Wrap = %q, want exactly one newline before the closing fence: %q", got, want)
	}
}

func TestWrapKeepsLeadingNewlines(t *testing.T) {
	got := fence.Wrap("\n\nbody")

	if want := "```\n\n\nbody\n```"; got != want {
		t.Errorf("Wrap = %q, want the leading newlines kept: %q", got, want)
	}
}

func TestWrapOfEmptyContentIsAFenceAroundOneEmptyLine(t *testing.T) {
	got := fence.Wrap("")

	if want := "```\n\n```"; got != want {
		t.Errorf("Wrap(\"\") = %q, want %q", got, want)
	}
}

func TestWrappedTextEndsWithTheClosingFenceAndNoNewline(t *testing.T) {
	got := fence.Wrap("body")

	if !strings.HasSuffix(got, "```") {
		t.Errorf("Wrap = %q, want it to end with the closing fence", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("Wrap = %q, want no newline after the closing fence", got)
	}
}

// --- Both directions together ---------------------------------------------

func TestWrappedContentContainingAFenceComesBackWhole(t *testing.T) {
	// The two directions share one rulebook, so quoting a file that fences
	// something of its own must survive the round trip.
	content := "Example:\n```go\nfoo()\n```\nEnd."

	assertOneBlock(t, fence.Wrap(content), content)
}
