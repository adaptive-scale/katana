# What this part of the product does

This part runs a project's configured test command, reports the command's outcome and recovered test cases, and records the result for status and history views. It supports whole-suite runs and runs narrowed to a single behavior.

## Running a test command

- A run may supply a command of its own, which replaces the configured test command for that run; a run that supplies none uses the configured command.
- If the configured test command is empty or contains only whitespace, no command is run and the error is `no test.command set in katana.yaml`.
- A supplied command is trimmed of surrounding whitespace, and is narrowed, made verbose and extended with extra arguments exactly as a configured command is.
- The command runs from the project's configured root, or from the root joined with the configured test directory when one is set.
- The command runs through the platform shell, using `cmd /C` on Windows and the configured `SHELL` with `-c` elsewhere; when `SHELL` is empty, `/bin/sh` is used.
- Extra arguments are appended to the command, with each argument enclosed in single quotes and embedded single quotes escaped so that spaces and quotes remain part of the argument.
- The caller's standard input is passed to the test command.
- Missing standard-output or standard-error writers are replaced with discard sinks.
- Standard output and standard error are captured for parsing even when no output writers are supplied.
- Captured output is also streamed to the supplied standard-output and standard-error writers; missing writers receive no output.
- When per-case reporting is requested and the configured framework supports verbose mode, the command is changed to its verbose form and the note `added -v to the test command so each case is recorded` is added.
- When per-case reporting is requested but the framework does not support verbose mode, the command is left unchanged and no verbose-mode note is added.
- When a target is supplied, the result scope is the target's behavior source.
- When the target can be represented by the configured framework, the command is narrowed to that target and the result reports that it was narrowed.
- When the target cannot be represented by the configured framework, the whole suite runs, the result reports that it was not narrowed, and the note `katana cannot narrow this runner to one behavior; running the whole suite and reporting <source>` is added.
- A command that starts and exits with a nonzero status returns a result rather than an error, and the result contains that exit code.
- An execution failure in which the command cannot be run returns an error beginning with `running test command:`.
- A successful command has exit code zero; a result is OK exactly when its exit code is zero.
- The result records the command that was actually run, its start time, its duration, and all captured output.

## Recovering and summarizing cases

- Recovered cases are parsed from the captured output using the configured framework, and `Parsed` is true exactly when at least one case is recovered.
- When streaming case observation is requested, the callback is invoked whenever parsing finds more completed cases than previously reported, with the complete currently recovered case list.
- A missing streaming-case callback causes no callback to be invoked.
- The recovered cases are counted as failures for failed status, skips for skipped status, and passes for every other status.
- Blocked recovered cases produce one blocked-suite name per distinct suite, in the order those suites first appear; non-blocked cases do not contribute names.
- Verbose Go output for an individually selected test suppresses lines containing `testing: warning: no tests to run`, lines ending in `[no tests to run]`, and standalone `PASS` lines in the streamed output.
- The suppressed no-tests output remains in the captured output used for reporting and parsing.

## Recording a run

- Recording creates or replaces the current results record with the run's command, start time, exit code, parsed flag, recovered cases, and scope.
- If an existing results record cannot be loaded, recording continues using the current run's outcome alone.
- A targeted run updates the targeted behavior's outcomes while inherited outcomes for other behaviors remain in the current results record.
- History stores one row for the run, including its duration in milliseconds, scope, exit code, whether per-case results were parsed, and totals based only on cases reported by that run.
- History behavior tallies are produced only for tracker entries that have at least one test, and include pass, fail, skip, and unknown counts for the cases attributed to each entry.
- If saving the results record fails, recording returns that error and does not proceed to history recording.
- If history recording fails after the results record is saved, recording returns the saved results together with the history error.

## Building a behavior target

- A target can be built only when the tracker contains an entry for the behavior source; otherwise no target is returned.
- A returned target contains the behavior source, its generated project-relative output, and the test names known by the tracker, including an empty test list when the entry has no tests.
