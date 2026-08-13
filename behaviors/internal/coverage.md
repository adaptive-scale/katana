# What this part of the product does

This part measures how much of a project's own code its tests execute. It does not instrument anything itself: it knows the arguments that turn coverage on for each test runner katana supports, and it reads the report those runners write, whichever of the three common formats it is in. What comes back has the same shape whatever produced it — statements counted and statements run, per file — so a Go project and a Python one are described the same way.

## Reading a coverage report

- The format of a report is decided by its contents, not by its file name.
- A report beginning with `mode:` is a Go cover profile; one beginning with `<?xml` or `<coverage` is Cobertura XML; one beginning with `TN:` or `SF:`, or containing a line beginning `SF:` or the text `end_of_record`, is LCOV.
- A report in none of those forms fails to parse with an error stating that it is not a Go cover profile, LCOV or Cobertura XML.
- Reading a report from a path returns the underlying file error when the file cannot be read, and otherwise an error naming that path when its contents cannot be parsed.
- A parsed report records which format it was read from, and a Go profile also records its cover mode.

## Reading a Go cover profile

- Each profile line is a file, a block of that file, the number of statements in the block and the number of times it ran.
- The file name of a profile line ends at its last colon, so a name containing one is read whole.
- A block's statements count once however many times the profile repeats that block, which is what a profile covering several packages does.
- A repeated block counts as covered when it ran in any of its occurrences.
- A file's statements are the sum over its distinct blocks, and its covered statements are the sum over the distinct blocks that ran.
- A line that is neither a mode line nor a well-formed block fails the whole report with an error naming the line number.

## Reading LCOV

- A record begins at an `SF:` line, which names its file, and ends at `end_of_record` or at the next `SF:` line.
- `DA:` lines are the file's lines and how often each ran; a `DA:` line whose count cannot be read counts as a line that did not run.
- Several records for one file are combined: the file's statements are its distinct line numbers, and a line counts as covered when it ran in any record.
- A record with no `DA:` lines falls back to its `LF:` and `LH:` totals, taking the largest value seen for each.
- Lines appearing outside any record are ignored.

## Reading Cobertura XML

- Every `class` element contributes its `filename` and its numbered lines with their hit counts.
- Two classes naming the same file are one file, with its lines merged.
- A file's statements are its distinct line numbers, and a line counts as covered when its hits are above zero.
- A hit count that is not a number counts as a line that did not run rather than removing the line.
- XML that cannot be parsed returns an error beginning with `parsing Cobertura XML:`.

## Summarising what was measured

- A file's coverage percentage is its covered statements as a percentage of its statements; a file with no statements reports zero.
- A file's missed statements are its statements less its covered ones.
- The total of a report is the sum of every file's statements and covered statements, under an empty path.
- A report is empty exactly when its total statements are zero.
- Grouping by directory sums the files in each directory, names the top level `.`, and returns the groups ordered by name.
- Ordering by coverage puts the least covered first; equal percentages are ordered by the most missed statements, and files still equal are ordered by path.

## Combining repeated files

- Merging combines entries with the same path into one, keeping the largest statement count and the largest covered count reported for it.
- A merged entry never reports more covered statements than statements.
- Merged entries are returned ordered by path.

## Naming files inside the project

- An absolute path inside the project is rewritten relative to the project root, with forward slashes.
- An absolute path outside the project is left absolute, with forward slashes.
- A path that is not absolute is rewritten to its longest trailing part that names an existing file under the project root, so an import path recorded by a Go profile becomes the project-relative file it refers to.
- A path that resolves to no file under the root is left as it was, cleaned.
- Two paths that resolve to the same file are merged into one entry.

## Turning coverage on for a runner

- The runner is taken from the configured framework where one is set, and recognised from the test command otherwise.
- `go`, `golang`, `go-test`, `gotest` and `gotestsum`, or a command running `go test`, mean the Go runner; `pytest`, `py.test`, `python` and `py`, or a command running `pytest` or `py.test`, mean pytest; `jest` and `react-scripts` mean jest; `vitest` means vitest; `mocha` means mocha; `node`, `node-test` and `node:test`, or a command running `node --test`, mean Node's own test runner; `cargo`, `cargo-test` and `rust`, or a command running `cargo test`, mean Rust.
- For the Go runner, coverage is `-coverprofile` pointing at `coverage.out` in the destination directory, read as a Go profile.
- For the Go runner, a `-coverpkg` pattern is appended when one is given and left out when it is empty.
- For the Go runner, a test command that already writes a coverage profile is instrumented anyway, with a note that katana's own profile takes precedence.
- For pytest, coverage is `--cov` and `--cov-report=xml:` pointing at `coverage.xml` in the destination directory, read as Cobertura XML, and it depends on the pytest-cov plugin.
- For jest, coverage is `--coverage`, `--coverageReporters=lcovonly` and `--coverageDirectory` set to the destination directory, read as LCOV from `lcov.info` in it.
- For vitest, coverage is `--coverage.enabled=true`, `--coverage.reporter=lcovonly` and `--coverage.reportsDirectory` set to the destination directory, read as LCOV from `lcov.info` in it, and it depends on a vitest coverage provider.
- For Node's own runner, coverage is `--experimental-test-coverage` with the lcov reporter written to `lcov.info` in the destination directory, and the spec reporter is asked for again so that per-case output is not lost.
- For mocha, the test command is replaced by that command run under `npx nyc` reporting lcov into the destination directory, and it depends on nyc.
- For Rust, the test command has its `cargo test` replaced by `cargo llvm-cov`, keeping the command's own flags, with `--lcov` and `--output-path` pointing at `lcov.info` in the destination directory; it depends on cargo-llvm-cov.
- A Rust project whose command already runs `cargo llvm-cov` keeps that command and only has the lcov output arguments appended.
- A Rust project whose command already runs `cargo tarpaulin` keeps that command, is given `--out xml` and `--output-dir`, and its report is read as Cobertura XML from `cobertura.xml` in the destination directory.
- Arguments meant for a runner underneath an npm script are preceded by `--`, and are passed directly for a command that is not an npm script.
- A directory katana puts into a command it assembled is single-quoted, with embedded single quotes escaped.
- A runner katana has no coverage arguments for is reported as unknown, with no command, no arguments and no report path.

## Finding a report another tool wrote

- A project is searched for a coverage report at `coverage.out`, `coverage/lcov.info`, `lcov.info`, `coverage/coverage.xml`, `coverage.xml`, `coverage/cobertura-coverage.xml`, `cobertura.xml` and `target/llvm-cov/lcov.info`, in that order, and the first one found is returned.
- A directory with one of those names is not a report.
- A project with none of them reports that nothing was found.

## Recording coverage history

- Coverage history is local project state at `.katana/coverage-history.json`, with schema version 1.
- Each successfully read report appends an observation with its UTC timestamp, format, optional Go mode, and each file's path, statement count and covered count.
- An instrumented suite run also records its command, exit code and duration; an observation read through `--profile` is marked as imported and records the displayed profile path instead.
- A suite command which ran but produced no readable report is still appended with its execution details and the report error, but no measured files.
- A command which never starts, an unknown runner, and a profile which cannot be read produce no history observation.
- Every compact observation is retained; coverage history does not trim an older detail window.
- Raw coverage profiles are never embedded in history and remain opt-in through `--save`.
- Adding an observation converts its timestamp to UTC, merges duplicate file paths, and keeps every previous observation.
- A missing history file loads as an empty version-1 history; observations loaded from a file are ordered oldest first by timestamp.
- A malformed or unsupported history file returns an error naming the file and does not get overwritten by a new observation.
- Saving creates `.katana` when needed and replaces the history file only after a complete indented, newline-terminated JSON document has been written and closed.
- Coverage history and its temporary files are added to `.katana/.gitignore` by `katana init` because they describe runs on one machine.

## Coverage statistics and comparisons

- A history's run count includes empty and failed observations, while percentage statistics include only observations which measured at least one statement.
- Statistics report the arithmetic average, minimum and maximum total coverage percentages, plus the first and last measurable timestamps.
- Recent history returns the requested number of newest measurable observations, oldest first; a non-positive or oversized request returns all measurable observations.
- The newest two measurable observations are available as a comparison only when both exist.
- File changes compare only paths which occur with statements in both observations; added, removed and statement-free files are not described as improvements or regressions.
- A file change is its newer percentage less its older percentage in percentage points; unchanged files are omitted.
- File changes are ordered from the largest regression to the largest improvement, then by path.
