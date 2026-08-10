# What this part of the product does

katana turns a behavior specification into tests by shelling out to an existing coding-agent command-line tool rather than calling a model API itself, so generation inherits that tool's authentication, model choice and tool permissions. This part decides which agent command to run, how the prompt reaches it, how long it may take, and how failures are reported back to the user.

## Built-in harnesses

- Five harnesses are built in: "claude", "codex", "opencode", "pi" and "hermes".
- The list of built-in harness names is returned in alphabetical order: claude, codex, hermes, opencode, pi.
- The descriptive listing used by the `katana harnesses` command returns the full built-in specs sorted by name.
- "claude" runs the executable `claude` with the arguments `-p --permission-mode auto`, delivers the prompt on standard input, uses `--model` for model overrides, and is described as "Claude Code CLI, non-interactive print mode, auto permissions".
- "codex" runs the executable `codex` with the argument `exec`, delivers the prompt as a final positional argument, uses `--model`, and is described as "Codex CLI, non-interactive exec mode".
- "opencode" runs the executable `opencode` with the argument `run`, delivers the prompt as a final positional argument, uses `--model`, and is described as "opencode CLI, single-shot run mode".
- "pi" runs the executable `pi` with the argument `-p`, delivers the prompt on standard input, uses `--model`, and is described as "pi CLI, non-interactive prompt mode".
- "hermes" runs the executable `hermes` with the argument `-p`, delivers the prompt on standard input, uses `--model`, and is described as "hermes CLI, non-interactive prompt mode".
- Looking up a built-in harness ignores surrounding whitespace and letter case, so " CLAUDE " resolves to the "claude" spec.
- A name that is not one of the five built-ins is reported as not found by the lookup.

## Choosing and configuring a harness

- A known harness name starts from its built-in spec, and any supplied overrides are applied on top of it.
- An unknown harness name is accepted as long as an explicit command is supplied; the resulting harness runs that command with no preset arguments, delivers the prompt on standard input, and uses `--model` as its model flag.
- An unknown harness name with no explicit command is rejected with the error `unknown harness "<name>"; built-in harnesses are claude, codex, hermes, opencode, pi (or set harness.command to use another agent CLI)`.
- A supplied command replaces the built-in executable; an empty command leaves the built-in executable in place.
- A supplied argument list replaces the built-in arguments, including when the supplied list is empty-but-present, which results in no preset arguments.
- Omitting the argument list entirely leaves the built-in arguments in place.
- A supplied prompt-delivery mode replaces the built-in one; an empty value leaves the built-in mode in place.
- A supplied model flag replaces the built-in one; an empty value leaves the built-in flag in place.
- The resolved specification is readable afterwards for diagnostics.

## Defaults applied at configuration time

- When no timeout is given, or the timeout is zero or negative, a single invocation is bounded at 10 minutes.
- When no diagnostics writer is given, harness diagnostics go to standard error.

## Redirecting verbose output

- A harness can be copied with a different diagnostics writer, leaving the original unchanged, so several harnesses running at the same time can each collect their output separately instead of interleaving on one terminal.

## Checking the executable is present

- Availability succeeds when the harness executable is found on the PATH.
- When the executable is not on the PATH, the reported error is `harness "<name>" needs "<command>" on your PATH but it was not found:` followed by the underlying lookup failure.
- Running a harness checks availability first and returns the same not-found error without starting any process and without producing a result.

## Invoking the harness

- The command line is the harness's configured arguments, in order.
- When a model override is set and the harness has a model flag, the model flag and its value are appended after the configured arguments.
- When a model override is set but the harness has no model flag, no model arguments are added.
- When no model override is set, no model arguments are added even if the harness has a model flag.
- In prompt-as-argument mode, the prompt is appended as the last argument, after any model arguments.
- In prompt-on-standard-input mode, the prompt is written to the process's standard input and does not appear on the command line.
- The process runs in the configured working directory.
- The invocation is abandoned once the configured timeout elapses.

## Environment passed to the harness

- When no extra environment entries are configured, the harness inherits the parent environment unchanged.
- When extra environment entries are configured, the harness receives the parent environment plus those entries, so an entry with the same name as an inherited one takes effect over it.
- Extra environment entries are added in alphabetical order of their names.

## What an invocation reports

- A successful invocation returns the harness's captured standard output, its captured standard error, and how long the invocation took.
- The captured output is returned even when the invocation fails, alongside the error.
- In verbose mode the harness's standard output and standard error are also streamed to the diagnostics writer while the process runs, in addition to being captured.
- In non-verbose mode nothing is streamed; output is only captured.

## Failure reporting

- When the invocation exceeds its time limit, the error is `harness "<name>" timed out after <timeout> (raise harness.timeout in katana.yaml)`.
- When the harness exits with a non-zero status, the recorded exit code is that status and the error is `harness "<name>" exited with status <code>: ` followed by an excerpt of the harness's standard error.
- Any other failure to run the process is reported as `running harness "<name>": ` followed by the underlying cause.
- The recorded exit code stays 0 for timeouts and for failures that are not a process exit.

## The standard-error excerpt in exit errors

- Leading and trailing whitespace is stripped from the harness's standard error before it is excerpted.
- Standard error that is empty, or only whitespace, is reported as `(no stderr output)`.
- Standard error of 10 lines or fewer is included in full.
- Standard error of more than 10 lines is truncated to its first 10 lines followed by a line containing `  ...`.

## Permission-denial hint

- A hint is produced when the harness's combined standard output and standard error contains, ignoring case, any of "denied", "not allowed", "no write access", "read-only", "grant write" or "without permission".
- No hint is produced when none of those markers appears — in particular, the bare word "permission" does not trigger a hint, because harnesses echo back the permission flag katana puts on the command line.
- For any harness other than "claude", the hint is `hint: the harness looks like it was denied file-write permission; grant it write access to the output path, e.g. via harness.args in katana.yaml`.
- For the "claude" harness, the hint is `hint: the harness looks like it was denied file-write permission; run it with write access, e.g. harness.args: ["-p", "--permission-mode", "auto"] in katana.yaml`.
