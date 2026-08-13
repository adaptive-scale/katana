# What this part of the product does

This part renders terminal-oriented status, result, history, and table output. It is used by callers that write to terminals, files, pipes, pagers, or full-screen views, and can include ANSI color when the destination and selected mode allow it.

## Color and visible text

- Color mode parsing trims surrounding whitespace and ignores case: an empty value or `auto` selects automatic mode, `always`, `force`, `yes`, and `true` select forced color, and `never`, `no`, and `false` select plain output.
- An unrecognized color mode returns `invalid colour mode %q (want auto, always or never)`, with the original input substituted for `%q`, and selects automatic mode as the returned mode.
- Automatic color is disabled when `NO_COLOR` is non-empty, enabled when `CLICOLOR_FORCE` is non-empty except for exactly `0`, disabled when `TERM` is exactly `dumb`, and otherwise follows whether the destination is a terminal.
- Forced color is enabled for every destination, and plain mode is disabled for every destination.
- A non-file writer, a file whose status cannot be read, or a file that is not a character device is not treated as a terminal.
- Colored text is wrapped with an ANSI SGR prefix containing the requested style codes joined by semicolons and a reset suffix; empty text or no styles is returned unchanged.
- Removing ANSI SGR color sequences leaves the other text unchanged, and visible width counts Unicode runes after those sequences are removed.
- Right or left padding adds spaces only when the visible text is shorter than the requested width; text already at or beyond that width is unchanged.
- Truncation returns an empty string for a non-positive limit, preserves text that already fits, and otherwise returns plain text ending in `…`; a one-column limit returns only `…`.

## Status and result display

- Up-to-date status is green, new status is cyan, behavior- or configuration-changed status is yellow, missing output is red, modified or untracked output is magenta, and any other status is grey.
- An unknown case is shown as a dim `•`; a failed known case is a red `✗`, a skipped known case is a yellow `○`, and any other known case is a green `✓`.
- A tally with no known cases is shown as a dim `-`.
- A known tally is shown as passed cases over total cases; it is red if any case failed, green if all total cases passed, and yellow otherwise.
- A run sparkline emits one block per run, with height based on the run rate and color grey for no known cases, red for failures, yellow for skips without failures, and green when there are passes without skips or failures.
- A behavior sparkline emits a block only for runs containing that behavior; its block height and color use that behavior's rate and outcome.
- A coverage sparkline emits one block per measurable coverage observation, with height based on the total percentage and color red below 50, yellow from 50 to below 80, and green from 80 upward.
- A zero or negative sparkline fraction uses `▁`, a fraction at least one uses `█`, and intermediate fractions use one of the eight blocks from `▁` through `█`; every positive intermediate fraction uses at least `▂`.
- A zero timestamp is shown as `-`; a future timestamp or one at least seven days old is shown as the local date in `YYYY-MM-DD` form.
- A timestamp less than one minute old is `just now`; timestamps within an hour, day, or seven days are shown as integer minutes, hours, or days followed by ` ago`.

## Bars and history

- A horizontal bar with non-positive width is empty; otherwise its fraction is clamped to 0 through 1 and it contains `█` for the filled portion and `░` for the remainder.
- Bar fill rounds to the nearest column, but every positive fraction retains at least one filled column and every fraction below one retains at least one unfilled column.
- A stacked bar with non-positive width or a zero total is entirely `░` for the non-negative width requested.
- Each nonzero stacked-bar segment receives at least one column, columns are allocated in count proportions, and excess columns caused by that minimum are removed from the widest segments while they exceed one column.
- A segment with no drawing character uses `█`; unused columns are dim `░`, and each segment is colored with its requested style.
- History bars color passed segments green, failed segments red and draw them with `▓`, and skipped segments yellow and draw them with `▒`.

## Tables

- A table with no header and no rows produces no lines; otherwise it includes a top rule, a bottom rule, and a row for every supplied data row.
- The number of columns is the larger of the header count and every row count; short rows display empty cells and longer rows add columns.
- Column widths use visible text width, include two spaces of cell padding, and are widened to the longest header or cell before rendering.
- Requested right-aligned columns place their cell contents against the right side of the column; all other cells are left-aligned.
- A positive maximum width reduces the widest columns until their text widths fit the available width after table framing, but never reduces a column below six visible columns; if that is insufficient, the table remains wider than the limit.
- A selected row is the row at the requested zero-based row index; a negative selection selects no row, and a row outside the data rows is not selected.
- Selected rows have their cell color removed and the complete row is rendered with reverse video; non-selected header text is bold and table rules and separators are dim.
- Cells longer than their allocated columns are truncated with `…`, while color is retained for non-selected cells and omitted from selected cells.
- Writing rendered lines stops at the first writer error and returns that error.

## Terminal dimensions

- Terminal width is returned only when the destination is an operating-system file whose terminal size can be read and is positive; otherwise it returns zero.
- Terminal size returns width, height, and success only when both dimensions are positive; any size-read error or non-positive dimension returns `0, 0, false`.
