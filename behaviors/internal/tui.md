# What this part of the product does

The terminal UI presents a project's configured behaviors, their generated test status, recorded results, and run history in a full-screen terminal interface. It accepts keyboard input to navigate, inspect, reload, run, stop, and quit; it can also render one non-interactive snapshot to a supplied writer.

## Starting and loading the interface

- Interactive mode is rejected when standard output is not a terminal with the error `katana tui needs a terminal (stdout is not one); try \`katana status\`, or \`katana tui --snapshot\``.
- If the terminal size cannot be read, interactive mode uses a width of 80 and a height of 24.
- If putting the input terminal into raw mode fails, interactive mode returns an error beginning with `putting the terminal in raw mode: ` followed by the underlying error.
- Interactive mode uses the terminal's alternate screen and hides the cursor while it is active, then shows the cursor, leaves the alternate screen, and restores the prior terminal mode when it ends.
- The interface reloads the tracker, planned behaviors, results, and history when it starts and after each successfully completed run.
- A tracker-loading or plan-building error prevents the normal data from loading and is shown in the behavior list.
- An unreadable results file leaves the behavior list available, replaces the results with empty results, and reports `katana: ` followed by the read error.
- An unreadable history file leaves the behavior list available, replaces the history with empty history, and reports `katana: ` followed by the read error.
- Snapshot mode returns a loading error instead of writing a frame when the initial tracker or plan load fails.
- Snapshot mode writes each rendered line without trailing spaces, followed by a newline, and returns a writer error if writing fails.

## Keyboard input and navigation

- Enter and newline activate the same action, as do both backspace bytes; arrow keys, Home, End, Page Up, and Page Down are recognized from their CSI and SS3 terminal sequences.
- A bare Escape is an Escape key; an incomplete or unknown escape sequence is consumed without performing a UI action.
- Alt-key sequences, mouse reports, function-key sequences, and other unsupported escape sequences are consumed and ignored rather than treating their contained characters as ordinary keys.
- Invalid UTF-8 input is consumed one byte at a time as a rune key using that byte's value.
- In the behavior list, Up/Down and `k`/`j` move the selection, Home/`g` select the first behavior, and End/`G` select the last behavior.
- Selection movement stops at the first and last behavior and does not wrap; with no behaviors, movement has no effect.
- Page movement changes the selection by the terminal height minus 12, with a minimum step of 1.
- Enter, Right Arrow, and `l` open the selected behavior; they do nothing when no behavior is selected.
- In a behavior detail or output view, Escape, Left Arrow, and `h` return to the list and reset scrolling.
- In detail and output views, Up/`k` scroll up without going below zero, Down/`j` scroll down, Page Up scrolls up by 10 without going below zero, and Page Down scrolls down by 10.
- In the help view, Escape, Enter, Left Arrow, `?`, and `h` return to the list.
- `?` opens the help view from the other views where it is not assigned a more specific navigation action.
- `q` quits when no run is active; while a run is active it reports `a run is going — press x to stop it, then q to leave` and does not quit.
- Ctrl-C stops an active run, or quits when no run is active.
- When terminal input ends, interactive mode returns normally.

## Running and stopping tests

- `r` runs the selected behavior from the list or detail view, while `a` runs the whole suite.
- `r` in the output view reruns the whole suite when the last run covered the whole suite; otherwise it runs the selected behavior.
- A selected behavior that has not been generated reports `<source> has not been generated yet — run \`katana generate\` first` and does not start a run.
- No run starts when the configured test command is blank or whitespace; the UI reports `no test.command set in katana.yaml, so there is nothing to run`.
- Starting a run while another run is active is rejected with `a run is already going; press x to stop it`.
- Runs execute with per-case reporting and capture both standard output and standard error for live display.
- Starting a run switches to the output view, clears the previous run result and error, resets output scrolling, and clears the message line.
- While a run is active, its output is displayed as it arrives, a spinner and elapsed time are shown, and only the most recent 2,000 complete output lines are retained.
- A carriage return discards the currently accumulated partial output line, and a newline commits the accumulated line; terminal styling is stripped from displayed output.
- Output that has not ended with a newline is displayed as a partial final line when it is non-empty.
- The output view follows the newest output while a run is active; after completion, its scroll position remains available for reading the captured output.
- `x` cancels an active run and reports `stopping the run…`; if no run is active it has no effect.
- Output already produced before a stopped run finishes remains visible.
- A run error leaves the run marked finished and reports `run failed: ` followed by the error; it does not record a result or replace the loaded project data.
- If recording a successful run fails, the UI reports `recording the run: ` followed by the recording error.
- A successful run reports its scope, passed or failed verdict, rounded duration, and parsed pass/fail/skip counts when per-case results were parsed.
- A successful run without parsed per-case results reports its scope, verdict, rounded duration, and `(no per-case results in its output)` instead of counts.
- A nonzero run exit code is reported as `failed (exit <code>)`; a zero exit code is reported as passed.
- Notes returned by a successful run are appended to its message, separated by ` · `.

## Displaying project state

- The list shows every planned behavior, its status, mapped case count, passed count, and, when the terminal is wide enough, test output, recent history, and generated age.
- A mapped tracker entry uses the number of test names when any are present; otherwise it uses the stored test count.
- An unmapped behavior displays `-` for cases, passed, and generated age, and has no recent sparkline.
- A behavior detail view shows the behavior's status and stack, generated age and harness when mapped, and otherwise `never — run \`katana generate\``.
- A detail view shows `no test cases recorded for this behavior` when the behavior is unmapped or has no recorded test cases.
- Each recorded case shows its known outcome mark, name padded for display, and either its recorded age or `never run`.
- Behavior history is shown newest first, up to 12 runs; an empty history displays `nothing recorded yet — the chart fills in as \`katana run\` is used`.
- Whole-suite history displays `nothing recorded yet — run the suite to start the chart` when no runs are recorded.
- The history chart displays as many recent runs as fit the terminal width and states whether the display contains all recorded runs or only a subset.
- A terminal narrower than 72 columns omits recent history, narrower than 90 omits generated age, and narrower than 108 omits the test-output column.
- On terminals narrower than 100 columns, the project counts use compact wording; on wider terminals they identify stale behaviors as `out of date` and current projects as `all up to date`.
- The frame is truncated to the terminal width and height; when content is too tall, the final key-hint line is preserved.
- A list longer than its available body shows a `showing <first>–<last> of <total>` indicator, and the title and footer remain fixed while rows scroll.
- An empty project displays `no behaviors configured — write one under behaviors/ and run \`katana generate\`` and only the quit hint.
- Reloading with `u` rereads tracker, results, and history and reports `reloaded`.
- `o` opens the output view when a run has been started from this interface; otherwise it reports `nothing has been run from here yet`.

## Leaving the interface

- The output view offers stop and scroll controls during a run, and run-again, scroll, back, and quit controls after a run finishes.
- The list, detail, output, and help views each provide a quit action, and leaving is blocked until an active run is stopped.
