# What this part of the product does

This is katana's test report: it captures the output of whatever test command a project configures, recovers individual test cases from that output where it can, and writes a self-contained, timestamped HTML page for the run. It is used by `katana run --save`, so a suite's results outlive the terminal scrollback.

## Making the runner print per-case results

- Given a command containing any of the characters `|`, `&`, `;`, `<`, `>`, a backtick, `$`, `(` or `)`, the command is returned unchanged and reported as not adjusted.
- A `go test` command with no verbosity flag is returned with ` -v` appended, and reported as adjusted.
- A `go test` command that already contains `-v`, `-json` or `-test.v` is returned unchanged and reported as not adjusted.
- A `pytest` or `py.test` command with no verbosity flag is returned with ` -v` appended, and reported as adjusted.
- A `pytest` command already carrying `-v`, repeated `-v`s such as `-vv`, `-q`, repeated `-q`s, `--verbose`, `--quiet` or `--tb` is returned unchanged.
- A flag counts as already present only when it starts the command or follows whitespace, and is followed by whitespace, `=` or the end of the command.
- Trailing whitespace is removed from the command before ` -v` is appended.
- A command is adjusted when either the configured framework names that runner or the command itself invokes it; the framework alone is not enough — a framework of `go` with a command that does not invoke `go test` leaves the command unchanged.
- When the framework names Go but the command runs pytest, the command is left unchanged: only the framework's own branch is considered.
- A command for any other runner (for example a shell script or make target) is returned unchanged.

## Capturing suite output

- Everything written through the recorder is forwarded unchanged to the underlying writer, so the terminal still streams the suite output live.
- The recorder keeps at most 8 MiB (8,388,608 bytes) of output; the write that crosses the limit is recorded up to the limit and the remainder is dropped.
- Once output has been truncated, the recorded text ends with the line `… output truncated by katana at 8 MB …`, surrounded by blank lines.
- Output that stays under the limit is returned exactly as written, with no truncation notice.
- Writes coming from separate streams (a process's stdout and stderr) can be recorded concurrently without corrupting the recorded text.
- A recording failure never changes what the caller sees: the write's byte count and error come from the underlying writer.

## Recognised frameworks and how a parser is chosen

- The frameworks katana can recover per-case results from are reported as: go-test, pytest, jest, vitest, mocha, cargo-test, xunit and xctest.
- The configured framework name is matched case-insensitively and with surrounding whitespace ignored.
- `go`, `golang`, `go-test`, `gotest` and `gotestsum` all select the Go parser.
- `pytest`, `py.test`, `python` and `py` all select the pytest parser.
- `jest`, `vitest`, `mocha`, `jasmine`, `ava`, `javascript`, `typescript`, `js` and `ts` all select the JavaScript parser.
- `cargo`, `cargo-test` and `rust` all select the Cargo parser.
- `xunit`, `nunit`, `mstest`, `dotnet` and `csharp` all select the .NET parser.
- `xctest`, `swift-testing` and `swift` all select the XCTest parser.
- Before parsing, terminal colour escape sequences are removed and both `\r\n` and lone `\r` are treated as line breaks, so coloured output parses the same as piped output.
- The configured framework's parser is tried first; its result is used whenever it recovers at least one case.
- If the configured framework is unknown, or its parser recovers nothing, every parser is tried in the fixed order go, pytest, js, cargo, dotnet, xctest, and the one that recovers the most cases wins.
- When two parsers recover the same number of cases, the one earlier in that fixed order wins, so the same output always produces the same report.
- Output no parser recognises yields no cases at all.

## Reading `go test` output

- A line of the form `--- PASS: Name (0.12s)` records a passing case; `--- FAIL:` records a failure and `--- SKIP:` records a skip.
- Result lines are recognised even when indented, so subtests are recorded as their own cases.
- A package summary line beginning `ok`, `FAIL` or `?` followed by a package path assigns that package as the suite of every case recorded since the previous package summary.
- A `FAIL` package line whose trailing text starts with `[` — such as a build failure note — adds an extra failed case in that package whose name is that text with the surrounding brackets removed, so a package that never ran its tests does not read as empty and passing.
- Detail printed between a `=== RUN` line and the case's result line is attached to that case, but only when the case failed or was skipped; a passing case carries no detail.
- Lines beginning `=== RUN`, `=== PAUSE`, `=== CONT` or `=== NAME` discard any detail buffered so far.
- A line that is exactly `PASS` or `FAIL` discards any detail buffered so far.

## Reading pytest output

- A line of the form `path/to/file.py::test_name PASSED` records a case whose suite is the file and whose name is the test.
- A short-summary line of the form `FAILED path/to/file.py::test_name - message` also records a case, and the text after ` - ` becomes its detail.
- `PASSED` and `XPASS` are recorded as passes; `FAILED` and `ERROR` as failures; `SKIPPED` and `XFAIL` as skips.
- When the same file-and-test pair appears more than once, the first occurrence sets the status; a later occurrence only fills in detail if none was recorded yet.
- If no per-case lines are found, the pytest parser reports nothing, even if the output has a FAILURES section.
- A FAILURES section header written as a name surrounded by three or more underscores on each side starts collecting a traceback for that case.
- Traceback collection ends at the next such header, or at the first line beginning `===`.
- A collected traceback is attached to the named case only if that case has no detail already.
- A header naming `TestClass.test_name` matches the case recorded as `TestClass::test_name`.
- A header that matches no case exactly is matched against cases by the last segment of the name after the final `.` or `:`.
- A header that matches no case at all is discarded and no detail is attached.

## Reading jest, vitest and mocha output

- A line starting `PASS`, `FAIL` or `RUNS` followed by a path sets the file that subsequent cases belong to.
- A line starting `✓` or `✔` records a passing case; `✕`, `✗`, `×` or `✘` records a failure; `○`, `◯`, `↓`, `⊘` or `✎` records a skip.
- A trailing `(123 ms)` on a case line is recorded as that case's duration; a case line without one has no duration.
- A line that carries one of those markers but also contains `(3 tests)` or `(1 test)` is treated as a per-file roll-up and is not recorded as a case.
- Leading and trailing whitespace on a line is ignored when matching, and the recovered case name is trimmed.
- Cases seen before any file line are recorded with an empty suite.

## Reading `cargo test` output

- A line of the form `test some::path::name ... ok` records a passing case; `... FAILED` records a failure and `... ignored` records a skip.
- A line matching `Running <target> (` sets the target that subsequent cases belong to, with surrounding whitespace trimmed.
- If no test result lines are found, the Cargo parser reports nothing.
- A `---- name stdout ----` header starts collecting the failure body printed for that case.
- Body collection ends at the next stdout header, at a line beginning `failures:`, or at the next test result line.
- A collected body is attached to the first case with that name that has no detail yet.
- Cargo cases carry no duration.

## Reading `dotnet test` output

- A line of the form `Passed Namespace.Class.TestName` records a passing case; `Failed` records a failure and `Skipped` records a skip.
- The fully qualified name is split at the last `.`: everything before it becomes the suite, the final segment the case name.
- A name with no `.` — or one whose only `.` is the first character — is recorded with an empty suite and the whole string as the name.
- A trailing bracketed timing such as `[12 ms]`, `[1 s]` or `[2 m]` is recorded as the case's duration.
- A `[< 1 ms]` timing is read as 1 ms: the leading `<` is ignored.
- A bracketed timing that is not exactly a number and a unit, whose number does not parse, or whose unit is not `ms`, `s` or `m`, is recorded as no duration.
- Leading and trailing whitespace on a line is ignored when matching.

## Reading XCTest output

- A line of the form `Test Case 'FooTests.testBar' passed (0.004 seconds)` records a case; `failed` records a failure and `skipped` records a skip.
- Both `seconds` and `second` are accepted in the timing.
- A name written as `-[FooTests testBar]` has its `-[` prefix and `]` suffix removed before being split.
- The name is split at the last space or `.`: the part before becomes the suite, the part after the case name.
- A name with no space or `.` is recorded with an empty suite.

## Failure detail attached to cases

- At most 200 lines of detail are kept per case; further lines are dropped.
- Blank lines are ignored until the first non-blank line of a case's detail.
- Trailing spaces and tabs are removed from each detail line.
- The common leading indentation shared by all non-blank detail lines is removed from every line.
- Blank lines at the start and end of the collected detail are removed.

## Timings

- A timing the runner reported as zero — such as `go test`'s `0.00s` for anything under 5 ms — is kept distinguishable from an absent timing and is displayed as `<1ms`.
- A timing that fails to parse is recorded as no timing.
- A case with no timing is displayed as an em dash rather than `0s`.
- A duration under 1 ms is displayed as `<1ms`.
- A duration under one second is displayed as whole milliseconds, for example `250ms`.
- A duration under one minute is displayed in seconds with two decimals, for example `12.34s`.
- A duration of one minute or more is rounded to the nearest second and displayed in the usual `1m30s` form.

## The recorded run and its totals

- When per-case results are recovered, the report is marked as parsed and lists those cases.
- When no parser recognises the output, the report records a single case standing for the whole suite: its suite is `test suite`, its name is the test command, and its duration is the suite's duration.
- That stand-in case is a pass when the command exited zero and a failure otherwise.
- The run's verdict comes from the command's exit code, not from the recovered cases: exit code zero reads `passed`, anything else reads `failed`.
- The case totals count passes, failures and skips among the recovered cases, including the stand-in case when nothing was parsed.
- The pass rate is the percentage of executed (non-skipped) cases that passed, and is zero when there are no executed cases.
- Cases are grouped into suites in the order the runner reported them, and a suite keeps its cases in that same order.
- A case whose suite is empty or only whitespace is grouped under `test suite`.
- Each suite reports its own pass, fail and skip counts and the sum of its cases' durations.
- The stale-behavior count is the number of recorded behaviors marked as out of date with their generated tests.

## Writing the report file

- The report is written into the given directory, which is created — along with any missing parents — if it does not exist.
- The file is named `report-` followed by the run's start time formatted as `YYYYMMDD-HHMMSS`, then `.html`, so each run leaves its own file and a directory of reports is a history of the suite.
- The path of the file written is returned.
- If the directory cannot be created, or the file cannot be written, no path is returned and the error is reported.
- The page is fully self-contained: its styling and interactivity are embedded, so it opens with no other files.

## What the page shows

- The header shows the project name, the run's start time formatted like `2 Jan 2006, 15:04:05 MST`, the run duration and the exit code.
- A verdict badge shows `passed` or `failed`, styled by which one it is.
- Tiles show the total case count, passes, failures, skips and the pass rate as a whole-number percentage.
- The proportional pass/fail/skip bar is shown only when there is at least one case.
- A details block lists the test command, the project root, the framework and the katana version.
- When no framework is configured, the framework is shown as `(unset)`.
- When per-case results could not be recovered, the page shows the note that katana could not recognise per-case results, that the result shown is the suite's exit code, that the full output is at the end of the page, and which frameworks per-case results are recovered for.
- Each suite is a collapsible section showing its name, its non-zero pass, fail and skip tallies and its total duration.
- A suite containing at least one failure is expanded when the page opens; a suite with no failures starts collapsed.
- Each case row shows its status as a coloured pill, its name, its failure detail when there is any, and its duration.
- The behaviors table is shown only when behaviors were recorded, and lists each behavior's source, generated output, status and stack.
- When any behavior was stale, the page shows a note giving the count of out-of-date behaviors, saying the results do not fully cover the current specification, and telling the reader to run `katana generate`.
- A behavior marked stale has its status highlighted.
- The full suite output is shown in a collapsible section at the end, and that section is omitted entirely when there is no output.
- The footer names the katana version that wrote the report.
- Values taken from the run — case names, failure detail, the command, suite output — are HTML-escaped, so runner output containing markup is displayed as text rather than rendered.

## Filtering within the page

- Buttons filter the case rows to all, failed, passed or skipped; the active button is marked as pressed and only one is pressed at a time.
- A search box filters case rows by name, matching a substring case-insensitively.
- The status filter and the name search apply together: a row is shown only when it satisfies both.
- A suite whose rows are all hidden is itself hidden.
- While any status filter other than "all" or any non-empty search term is active, every suite that still has a visible row is expanded.
- When nothing matches, the page shows `No test case matches this filter.`; that message is hidden whenever at least one row is visible.
- The search term is trimmed of surrounding whitespace before matching, so a term of only spaces matches everything.
