# What this part of the product does

This is the shared rulebook for markdown code fences: it pulls file contents out of the fenced blocks in an agent's reply, and it wraps file contents in a fence when katana quotes a file back to an agent. Both directions have to survive content that contains fences of its own, so the same rules apply either way.

## Collecting fenced blocks from text

- Given a piece of text, the collector returns the contents of every fenced block it finds, in the order the blocks appear.
- A block opens at any line whose text, ignoring leading and trailing whitespace, begins with three or more backticks.
- The opening fence line is never part of the returned content.
- Anything written after the backticks on the opening line — a language tag such as `go`, or any other info text — is discarded along with the rest of that line.
- Text that sits outside any fenced block, such as prose before or after a block, is not returned.
- Text with no fenced block at all returns no blocks.
- Empty text returns no blocks.
- Content lines are returned exactly as they appeared, including their original leading and trailing whitespace, joined by newlines.
- The returned content has no trailing newline of its own: a block containing a single line returns just that line.

## Closing a block

- A block closes at a line that, ignoring leading and trailing whitespace, consists only of backticks and has at least as many backticks as the line that opened it.
- The closing fence line is never part of the returned content.
- A closing line with more backticks than the opening line still closes the block.
- A line of backticks shorter than the opening fence is treated as ordinary content, not as a close — so a three-backtick line inside a block that was opened with four backticks is kept verbatim.
- A line that starts with enough backticks but has other text after them — for example a nested opening fence with a language tag — is treated as ordinary content, not as a close.
- After a block closes, scanning continues and later blocks in the same text are collected too.
- A block that opens and closes with nothing between the two fence lines is reported as a block whose content is the empty string, and it counts toward the number of blocks found.

## Blocks that are never closed

- A block that is opened but never closed is still returned, carrying every line from the opening fence to the end of the text.
- A block that is opened at the very last line, with no content lines after it, is dropped entirely rather than returned as empty.

## Picking the file body out of a reply

- When a reply fences several things, the longest block is taken to be the file and the shorter ones are treated as commentary.
- Text containing no fenced block yields the empty string.
- Length is compared by the number of characters in each block's content.
- When two blocks are the same length, the one that appears first in the text wins.
- A reply with exactly one fenced block yields that block's content, even if it is empty.

## Wrapping content in a fence

- Wrapping puts the content between an opening and a closing fence line, each on its own line.
- The fence is three backticks when the content contains no run of three or more backticks.
- When the content contains a run of backticks that would otherwise end the block early, the fence is lengthened until it no longer appears anywhere in the content — so content holding a four-backtick run is wrapped in a five-backtick fence.
- The lengthening looks at the whole content, including backtick runs that appear in the middle of a line, not only at line starts.
- Trailing newlines at the end of the content are removed before wrapping, so there is exactly one newline between the content and the closing fence.
- Leading newlines at the start of the content are kept as-is.
- Empty content produces a fence around a single empty line.
- The wrapped result ends with the closing fence and no newline after it.
