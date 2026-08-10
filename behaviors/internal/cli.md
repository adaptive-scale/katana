# What this part of the product does

This is katana's command line: the entry point a developer or a CI job invokes to set up a project, turn written product behavior into generated tests, run the resulting suite, inspect what is out of date, and upgrade the tool itself. It dispatches a subcommand, validates its flags, drives a coding-agent harness where one is needed, and reports what happened.

## Dispatching a command

- Invoking katana with no arguments at all prints the usage text to standard output and succeeds.
- `init`, `discover`, `generate`, `run`, `status`, `harnesses`, `update` and `version` are the recognised commands.
- `gen` is accepted as an alias for `generate`.
- `test` is accepted as an alias for `run`.
- `upgrade` and `self-update` are accepted as aliases for `update`.
- `--version` and `-v` are accepted as aliases for `version`.
- `help`, `--help` and `-h` all print the usage text to standard output and succeed.
- An unrecognised first argument prints the usage text to standard error and fails with `unknown command "<argument>"`.
- `version` prints `katana ` followed by the version string; when the binary was not stamped at build time that string is `dev`.
- The usage text lists the built-in harness names and the typical two flows: init, edit a behavior file, generate, run — and, for a codebase with no behaviors written yet, init, discover with `--dry-run`, then discover.
- Every command except `update`, `upgrade`, `self-update`, `help`, `--help` and `-h` starts a release check that runs alongside the requested work and prints its notice to standard error only after that work has finished, so it never delays the command.

## Setting up a project

- `init` defaults to the current directory, the `go` language, the `claude` harness, a `behaviors` directory for specifications, and a `tests` directory for generated output; each is overridable by flag.
- Naming a harness that has no built-in entry fails with `unknown harness "<name>"; choose one of <known names>, or set harness.command in katana.yaml to use another agent CLI`, and nothing is written.
- If the configuration file already exists, `init` fails with `<path> already exists (pass --force to overwrite)` and leaves it in place.
- `--force` overwrites an existing configuration file.
- The written configuration records the chosen harness name, language, the framework, output directory, output-file template and test command that are the defaults for that language, and the behaviors directory as a single behavior path.
- The configuration file is written with commented-out examples for every optional harness field — command, args, prompt delivery, model, model flag, timeout, jobs and environment — and for per-behavior overrides of output, language, framework, harness and extra instructions.
- `init` creates the tracker directory and an empty tracker file, reporting `created <tracker path>` when it made one and `kept existing <tracker path>` when one was already there.
- A `.gitignore` is placed in the tracker directory ignoring only the scratch files `.tracker-*.json`, so the tracker itself stays under version control; an existing `.gitignore` there is left untouched.
- The behaviors directory is created, and a sample behavior file named `example.md` is written into it unless `--no-sample` is given.
- The sample behavior file is not overwritten if a file of that name already exists.
- `init` finishes by reporting which harness and language it configured and telling the user to describe a behavior in the behaviors directory and then run generate.

## Discovering behavior from existing code

- `discover` reads the product code that already exists and writes behavior files describing it, mirroring the source tree into the behaviors tree.
- The language read defaults to the project's configured default language and can be overridden with `--language`.
- The destination defaults to the project's configured behaviors directory and can be overridden with `--out`.
- `--path` limits discovery to a file or subtree and may be repeated.
- `--exclude` skips a directory by name or path and may be repeated.
- `--group` selects the unit of discovery, defaulting to grouping by directory.
- Test code is left out of what is read unless `--include-tests` is given.
- When the scan finds no source, it reports `no <language> source found in this project` — or, when `--path` was given, `no <language> source found in <the paths, comma-separated>` — followed by advice to check the configured default language or pass `--language` and `--path`, and succeeds.
- A unit whose behavior file already exists is skipped and reported as `skip <name> → <output> (already written; pass --force to update it against the code)`.
- `--force` rewrites behavior files that already exist, and those units are labelled `update` rather than `new`.
- When every unit already has a behavior file, discover reports `all <n> unit(s) already have behavior files` and succeeds without running the harness.
- `--dry-run` lists each unit that would be discovered with its output path, file count and total size, then reports how many files would be read with which harness, and runs no harness.
- The harness is checked once before any unit is started, so an unavailable harness fails immediately rather than once per unit.
- Each finished unit reports its size in bytes, how the file arrived, and its elapsed time; the arrival is `written by harness`, `recovered from harness stdout` when the file had to be recovered from the harness's own output, or `unchanged; harness found nothing to correct`.
- A unit the harness declined reports `skipped: <reason>` and counts toward the summary line `, <n> unit(s) had no behavior to specify`.
- A unit that errored reports `failed: <error>` to standard error.
- The run ends with `wrote <n> behavior file(s)`.
- If any behavior file was written outside every configured behaviors path, discover notes how many, explains they will not be generated from, and suggests the `path:` entry to add under the behaviors configuration.
- A configuration that cannot be resolved produces no such note either way.
- After writing anything, discover tells the user to review it — it describes what the code does today, bugs included — before running generate.
- If any unit failed, discover exits with `<n> of <m> unit(s) failed`.

## Generating tests from behaviors

- `generate` produces tests only for behaviors that need it, judged against the tracker; everything else is left alone.
- `--file` limits the run to named behavior files and may be repeated; an absolute path is interpreted relative to the project root.
- When no behavior matches, generate prints `no behaviors matched` and succeeds.
- A behavior whose generated tests were edited by hand is skipped and reported as `skip <source> → <output> (<status>; pass --force to regenerate over it)`, so hand edits are never silently discarded.
- `--force` regenerates every matched behavior, including up-to-date and hand-edited ones.
- When nothing needs work, generate reports `all <n> behavior(s) up to date` and succeeds.
- `--dry-run` lists each behavior that would be generated with its status, source, output and language/framework/harness, and runs no harness.
- Every distinct harness the planned work needs is built and checked before any behavior is generated, so a missing or misconfigured agent CLI is reported before agent time is spent.
- Each finished behavior reports its size in bytes, the number of test cases found in the generated file when there is at least one, how the file arrived, and its elapsed time.
- The arrival is `written by harness`, `recovered from harness stdout`, or `unchanged; harness judged existing tests sufficient`.
- After each successful behavior the tracker entry — source, source hash, output, output hash, test names, language, framework, harness, generation time and katana version — is recorded and written to disk immediately, so an interrupted run keeps the work already done.
- A failure to write the tracker mid-run prints `warning: could not update tracker: <error>` to standard error and does not stop generation.
- When the run was not limited with `--file`, tracker entries for behaviors no longer in the configuration are removed and each is reported as `pruned tracker entry for removed behavior <source>`.
- If any behavior failed, generate exits with `<n> of <m> behavior(s) failed to generate`.
- If fewer behaviors were generated than planned without any failure — an interrupted run — generate reports `generated <n> of <m> behavior(s)` and succeeds.
- When all planned behaviors succeeded, generate reports `generated <n> behavior(s)`.
- If the generated file is missing after a generation that otherwise succeeded, that is not an error; it is recorded as an empty hash and surfaces as an out-of-date "output missing" state on the next status.

## Choosing how many run at once

- Both discover and generate accept `--jobs`, with `-j` as its shorthand.
- Explicitly passing a jobs value below 1 fails with `--jobs must be at least 1, got <value>` before any work starts.
- When `--jobs` is not given, the count comes from the project's configured harness jobs setting.
- A requested count larger than the number of items to do is reduced to that number.
- Passing `--verbose` without `--jobs`, when there is more than one item and the configured count is above one, runs one item at a time and prints `note: --verbose narrates one behavior at a time; pass --jobs N to generate in parallel`.
- With more than one worker, the run announces `<verb> <n> item(s), <k> at a time`; with one worker it announces just the count.
- With one worker, output streams to the terminal as it happens and each item is announced as `[<done>/<total>] <source> → <output> (<status>)` when it starts.
- With more than one worker, each item announces `start <source> → <output> (<status>)` immediately and its full narration is held back and printed as one block when it finishes, so concurrent agents never interleave mid-line.
- With more than one worker the `[<done>/<total>]` counter follows completion order, not the order work was queued.
- An interrupt (Ctrl-C or a termination signal) stops handing out new work while letting what is already running finish, and the run prints `interrupted; stopping`.

## Deciding whether a behavior is current

- A behavior with no tracker entry is `new`.
- A behavior whose specification file has changed since it was recorded is `behavior changed`, and that takes precedence over any hand edit to the generated file.
- A behavior whose recorded output path, language, framework or harness differs from the current configuration is `config changed`.
- A behavior whose generated file is absent is `output missing`.
- A behavior whose generated file differs from the recorded hash, with the specification unchanged, is `output modified`.
- A behavior matching its tracker entry in every respect is up to date.
- A recorded entry with no output hash never counts as modified.

## Reporting status

- `status` prints one row per matched behavior with its status, behavior file, generated tests file, and language/framework/harness.
- When no behavior matches the filter, status prints `no behaviors matched` and succeeds.
- `--file` limits the report to named behavior files and may be repeated.
- Status ends with `<n> behavior(s), <m> out of date`.
- When anything is out of date, status advises running generate.
- `--strict` makes an out-of-date behavior a failure, exiting with `<n> behavior(s) out of date`.

## Running the test suite

- `run` executes the test command from the project configuration through a shell, in the project root or in the configured test directory when one is set.
- A configuration with no test command fails with `no test.command set in katana.yaml`.
- Arguments after `--` are shell-quoted and appended to the test command, so paths and patterns containing spaces or quotes survive.
- Before running, any out-of-date behavior is listed on standard error with its status and source, followed by advice to run generate first, so a passing suite is never mistaken for one that covers the current specification.
- `--check` turns that warning into a failure, exiting with `<n> behavior(s) out of date (--check)` without running the suite.
- The suite's own standard input, output and error are connected to the terminal.
- If the test command fails to start at all, run fails with `running test command: <error>`.
- When the test command exits non-zero, katana exits with that same exit code so CI sees the real result.
- On Windows the command is run through `cmd /C`; elsewhere through the shell named by the `SHELL` environment variable, falling back to `/bin/sh`, with `-c`.

## Saving a test report

- `--save` writes a self-contained HTML report of the run, listing every test case with its outcome, its failure output, and which behaviors were out of date at the time.
- When katana knows how to make the configured framework's runner report per-case results, `--save` adds `-v` to the test command and notes `--save added -v to the test command so each case is recorded`.
- The suite's output is copied as it streams, so the terminal shows exactly what it would have without `--save`.
- The report is written into the `--out` directory, which defaults to `out` and is resolved against the project root unless it is an absolute path.
- On success run prints the report path together with the passed, failed and skipped counts.
- If the report cannot be written and the suite passed, run fails with `writing test report: <error>`.
- If the report cannot be written and the suite failed, the error is printed as a `katana:` line and the suite's own exit code is still what katana exits with.
- Each behavior recorded in the report carries its source, generated output, status, its language/framework/harness, and whether it was stale.

## Listing harnesses

- `harnesses` prints one row per known coding-agent harness with its name, whether it is installed, its default invocation, how the prompt is delivered, and a description.
- The installed column shows the resolved path to the executable when it is on the PATH, and `no` when it is not.
- The output explains that the defaults are katana's best-known invocation for each CLI rather than a contract with the upstream tool, and shows how to override the command, arguments and prompt delivery per project.

## Updating katana

- `update` replaces the running binary with the newest published release, verifying the release checksum when one is available, and reports `updated katana to <tag> (<path>)`.
- The whole update, download included, is bounded to five minutes.
- `--check` reports without installing: `katana <version> is up to date`, or `katana <tag> is available (you have <version>)` followed by the release URL when there is one and advice to run update.
- Without `--check`, an already-current version reports `katana <version> is already up to date` and installs nothing.
- `--force` reinstalls even when the running version is already current.
- `--version <tag>` fetches that specific release and always installs it, whether it is newer or older than the running one; the "already up to date" shortcut applies only when no tag was pinned.
- The command documents that katana checks for a release once a day in the background, that `KATANA_NO_UPDATE_CHECK=1` turns that off, that it is already off in CI and for locally built binaries, and that a private repository's releases need a token in `GITHUB_TOKEN` or `KATANA_GITHUB_TOKEN`.

## Narrating a run with --verbose

- `--verbose` on generate reports, before the harness starts, the specification being read with its size and line count, the target file, the language/framework stack, and the harness command line with how the prompt is delivered.
- The target is described as `new file` when nothing is there, and as `replacing <size>` when generate is about to overwrite an existing generated file.
- Any per-behavior extra instructions are shown as their first line only.
- `--verbose` on discover reports the unit name with its file count and total size, lists every file that will be read, names the target file — `new file`, or `updating <size>` when one exists — and shows the harness command line.
- The prompt katana sends is printed with its size and line count.
- After a generation, `--verbose` reports the written file's size, line count and the test cases katana can see in it, listing each test name; this is the same index that goes into the tracker.
- After a discovery, `--verbose` reports the written behavior file's size together with how many sections and how many statements it contains, counting lines beginning with `#` as sections and lines beginning with `- ` or `* ` as statements.
- If the written behavior file cannot be read back, `--verbose` falls back to reporting just its name and byte count.
- Anything the harness said on its own is shown as a `harness said:` line, first line only.
- Prompt and file previews are capped at 40 lines, with the remainder noted as `… <n> more lines …`.
- Quoted first lines are trimmed to 120 characters with an ellipsis, and a multi-line value is marked with a trailing ellipsis.
- Sizes below 1024 bytes are shown in bytes, below a megabyte in KB to one decimal place, and above that in MB to one decimal place.
- With more than one worker, `--verbose` sends the harness's own live output into that item's buffered block rather than to the terminal, so it stays with the rest of that item's narration.
