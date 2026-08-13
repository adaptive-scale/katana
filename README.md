# katana

Generate test code from written product behavior, and keep it in sync as those
behaviors change.

You describe what the product should do in plain-language markdown. katana turns
each behavior file into a test file, records what it generated, and on the next
run regenerates only the behaviors whose specification actually changed.

katana does not call a model API itself. It shells out to whichever coding-agent
CLI you already use — Claude Code, Codex, opencode, and others — so generation
inherits that agent's authentication, model choice, and tool permissions.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/adaptive-scale/katana/master/install.sh | sh
```

The script installs the release binary for your platform into `/usr/local/bin`,
or `~/.local/bin` when that is not writable. Where a release publishes
`checksums.txt`, the download is verified against it. If no release binary
matches your platform, the script builds from source with the Go toolchain.

```sh
install.sh --version v1.2.3    # pin a version instead of the latest release
install.sh --dir ~/bin         # choose the install directory
install.sh --source            # always build from source
```

`KATANA_VERSION` and `KATANA_INSTALL_DIR` do the same as `--version` and
`--dir`, which is handy when piping the script into `sh`:

```sh
curl -fsSL .../install.sh | KATANA_INSTALL_DIR=~/bin sh
```

With a Go toolchain, or on Windows:

```sh
go install github.com/adaptive-scale/katana@latest
```

Or build from a checkout:

```sh
go build -o katana .
```

### Staying up to date

katana updates itself in place:

```sh
katana update           # install the newest release over this binary
katana update --check   # report whether a newer release exists
katana update --version v1.2.3
```

The download is verified against the release's `checksums.txt` and swapped in
atomically, so a failed update leaves the working binary alone. If katana lives
somewhere you cannot write, re-run with `sudo`.

Once a day katana also asks GitHub for the newest release while your command
runs, and mentions it afterwards if you are behind. The check never delays or
fails a command. It is already off in CI and for locally built binaries; set
`KATANA_NO_UPDATE_CHECK=1` to turn it off everywhere.

## Quickstart

```sh
katana init --language go --harness claude
$EDITOR behaviors/example.md
katana generate
katana run
```

`init` scaffolds three things:

```
katana.yaml            # configuration
behaviors/example.md   # a sample behavior to edit or delete
.katana/tracker.json   # what was generated from what — commit this
```

`katana run` later adds `.katana/results.json`, the outcome of the last run that
`katana status` reports pass counts from, and `.katana/history.json`, a short
record of the runs before it that the charts are drawn from. `katana coverage`
adds `.katana/coverage-history.json`, containing compact coverage totals for
each observation. All three are local, and `init` writes a `.katana/.gitignore`
that leaves them out of version control.

Then the loop is: edit a behavior file, run `katana generate`, run `katana run`.
Or run `katana` on its own, which opens the whole project in one screen and runs
a behavior's tests where you are standing.

A codebase that already exists starts one step earlier: `katana discover` reads
the code and writes the behavior files it already implements, as a draft to
correct before generating from it.

## Writing a behavior

A behavior file is ordinary markdown. State what is observable, not how it is
implemented — each bullet becomes a test case.

```markdown
# Shopping cart checkout

## Applying a discount code

- A valid, unexpired discount code reduces the order total by its percentage.
- The reduction applies to the item subtotal, before shipping and tax.
- An order total never goes below zero, however large the discount.

## Rejecting an invalid code

- An unknown code leaves the total unchanged and reports "code not recognised".
- An expired code leaves the total unchanged and reports "code has expired".
```

`behaviors/checkout.md` generates `tests/checkout_test.go` by default. The output
path comes from `defaults.output_dir` and `defaults.output_template`.

## Commands

| Command | What it does |
| --- | --- |
| `katana` | Open the full-screen view of the project (same as `katana tui`) |
| `katana init` | Create `katana.yaml`, `.katana/`, and a sample behavior |
| `katana discover` | Write behavior files for the code this project already has |
| `katana generate` | Generate tests for behaviors that changed since the last run |
| `katana run` | Run the test command from `katana.yaml` |
| `katana coverage` | Run the suite with coverage on and report what it executed |
| `katana status` | Show what the tracker holds and which behaviors are out of date |
| `katana tui` | Behaviors, their results, and runs on demand, in one screen |
| `katana harnesses` | List the supported agent CLIs and whether they are installed |
| `katana update` | Install the newest release over this binary |
| `katana version` | Print the katana version |

Run `katana <command> --help` for a command's flags.

Output that is a table is drawn as one, and colour-coded: green is a behavior
that needs nothing done, yellow is work the next `katana generate` will do, red
is a failure or a generated file that is gone, and magenta is a test file katana
did not write and will not overwrite. Colour is used only when the output is a
terminal; `NO_COLOR`, `CLICOLOR_FORCE` and `--color auto|always|never` decide it
outright.

### discover

```sh
katana discover                          # a behavior file per source directory
katana discover --dry-run                # report what would be discovered, run nothing
katana discover --path internal/billing  # only this file or subtree (repeatable)
katana discover --exclude examples       # skip a directory, by name or path (repeatable)
katana discover --force                  # update existing behavior files against the code
katana discover --jobs 8                 # eight agents at once (-j for short)
```

discover is `katana generate` run backwards: it reads the code the project
already has and writes the behavior it implements into the behaviors directory,
so a codebase with no specifications has somewhere to start.

```sh
katana init --language go --harness claude
katana discover --dry-run
katana discover
```

Only one language is read — `defaults.language`, or `--language` — and only
product code: test files, dependencies, build output, generated files and
hidden directories are left out, and `--include-tests` brings the test code
back in. Source files are gathered one behavior file per directory — a package,
in most of the languages katana targets — and the behavior tree mirrors the
source tree, so `internal/billing` becomes `behaviors/internal/billing.md` and
the tests generated from it land under the matching directory. `--group file`
writes one behavior file per source file instead, for directories too large to
describe in one specification, and `--out` writes somewhere other than where
`katana.yaml` already keeps behaviors.

Each unit goes to the configured harness, which reads the source and writes the
specification the way generation writes tests: the file itself, with stdout as
the fallback. A unit whose source states no product behavior — constants, plain
data, a package that only wires other packages together — is skipped with the
harness's reason, rather than padded out into invented rules. Units are
discovered four at a time, under the same `harness.jobs` and `--jobs` as
generation, and `--verbose` narrates the files read, the prompt, and the
harness output as it arrives.

A behavior file that already exists is reported and left alone. `--force` asks
the harness to update it against the code as it now stands — correcting what
the code contradicts, keeping the wording of everything still true — so hand
edits survive; a file the harness found nothing to correct comes back unchanged
and says so.

What comes back is a draft. It describes what the code does today, including
whatever it does by accident, so read and correct it before running
`katana generate` — from then on the behavior file is the source of truth. A
discovered file that falls outside every `behaviors:` path in `katana.yaml` is
pointed out, along with the `- path:` line that would include it.

### generate

```sh
katana generate                          # only what changed
katana generate --dry-run                # report what would run, run nothing
katana generate --file behaviors/cart.md # one behavior (repeatable)
katana generate --force                  # regenerate everything
katana generate --jobs 8                 # eight agents at once (-j for short)
katana generate --verbose                # narrate each generation
```

The tracker is saved after each behavior succeeds, so an interrupted run keeps
the work already done. Behaviors deleted from the config have their tracker
entries pruned on a full run.

### Generating in parallel

Behaviors are generated four at a time by default. Each one waits on an agent
CLI rather than on this machine, so the useful number has nothing to do with the
core count — raise it for a repository full of behaviors, lower it if your agent
starts returning rate-limit errors. Set the project's own default with
`harness.jobs` in `katana.yaml`; `--jobs` overrides it for one run, and
`--jobs 1` generates one behavior after another.

Every behavior's output is printed as one block when it finishes, so concurrent
agents never interleave mid-line. A `start` line goes out as each behavior is
picked up, so it is clear what is in flight:

```
generating 6 behavior(s), 3 at a time
  start behaviors/cart.md → tests/cart_test.go (new)
  start behaviors/checkout.md → tests/checkout_test.go (behavior changed)
  start behaviors/refunds.md → tests/refunds_test.go (new)
[1/6] behaviors/checkout.md → tests/checkout_test.go (behavior changed)
  ok: 3271 bytes, written by harness, 41.2s
  start behaviors/search.md → tests/search_test.go (new)
```

A harness that is missing or misconfigured is reported before any behavior
starts, rather than after minutes of agent time. Individual failures do not stop
the rest — the run ends by naming how many behaviors failed.

`--verbose` shows what is being generated rather than only that something is.
It narrates one behavior at a time unless `--jobs` says otherwise, since live
narration only reads as narration when one generation is producing it:

```
[1/1] behaviors/cart.md → tests/cart_test.go (new)
  spec     behaviors/cart.md (1.4 KB, 38 lines)
  target   tests/cart_test.go (new file)
  stack    go / go-test
  harness  claude -p --permission-mode auto (prompt via stdin)
  prompt   4.1 KB, 96 lines
  │ You are generating an automated test suite from a written product behavior…
  running harness…
  …harness output streams here…
  wrote    tests/cart_test.go (3.2 KB, 118 lines, 7 test case(s))
    • TestValidDiscountCodeReducesTotal
    • TestExpiredDiscountCodeIsRejected
```

### run

```sh
katana run                                 # run the suite
katana run --check                         # fail if any behavior is out of date
katana run --save                          # also write an HTML report to out/
katana run --behavior behaviors/cart.md    # only the tests generated for one behavior
katana run -- -run TestCheckout            # arguments after -- are appended
```

`run` warns when a behavior has changed since its tests were generated, so a
green suite is never mistaken for one that covers the current specification.
`--check` turns that warning into a failure — the useful form in CI.

Every run records what each test case did to `.katana/results.json`, which is
what lets `katana status` report how many cases passed without running the suite
again, and appends a row to `.katana/history.json`, which is what the charts are
drawn from and what counts the runs: the file keeps the last fifty of them, and
carries running totals — how many runs there have been, how many passed, how
many case outcomes they reported and how long they spent in the runner — so the
count survives the rows being trimmed. Recovering results per case needs the
runner to name each one, which
some only do in verbose mode, so katana adds that flag where it knows it and says
so; `--cases=false` leaves the command exactly as configured and records the
suite-wide result alone. Both files describe one machine's runs, so they are
ignored by `.katana/.gitignore` rather than committed.

`--behavior` runs only the tests generated for one behavior, where katana knows
how to narrow the runner: by test name for `go test`, by file for pytest, jest,
vitest and mocha. Anything else runs whole and says so. The outcomes recorded for
the other behaviors are left standing rather than erased, so `katana status`
still knows how they last did — and says which of them are older than the last
run.

### Saving results as HTML

`katana run --save` writes `out/report-<timestamp>.html` — one file per run, so
the directory becomes a history rather than only the latest state. Use `--out`
to write somewhere else.

The page is self-contained (no network, no assets) and holds:

- the verdict, the suite's exit code, and how long it took;
- every test case with its outcome, timing and failure output, grouped by
  package, file or class, filterable by status and name;
- which behaviors were out of date when the suite ran, so a green report does
  not read as coverage of a specification it never saw;
- the full suite output.

Per-case results are recovered by reading the runner's own output, so the table
is as detailed as the runner is: go-test, pytest, jest, vitest, mocha,
cargo-test, xunit and xctest are parsed. Any other runner still gets a report of
the command, the exit code and the full output. Because `go test` and `pytest`
only name individual cases in verbose mode, katana appends `-v` to those two
commands and says so.

The suite's exit code is still propagated after the report is written, so
`--save` is safe to leave on in CI.

### coverage

```sh
katana coverage                            # run the suite with coverage on
katana coverage --by file --sort coverage  # per file, least covered first
katana coverage --min 80                   # fail below 80% — the CI form
katana coverage --save coverage.out        # keep the report the runner wrote
katana coverage --profile coverage.xml     # read a report instead of running
```

```
┌────────────────────┬───────┬─────────┬────────┬─────────────────────┐
│ PACKAGE            │ STMTS │ COVERED │ MISSED │ COVERAGE            │
├────────────────────┼───────┼─────────┼────────┼─────────────────────┤
│ internal/cli       │  1380 │    1084 │    296 │ █████████░░░  78.6% │
│ internal/config    │   286 │     270 │     16 │ ███████████░  94.4% │
│ internal/tui       │   511 │     322 │    189 │ ████████░░░░  63.0% │
└────────────────────┴───────┴─────────┴────────┴─────────────────────┘

total  79.7% of 4371 statement(s) — 3484 covered, 887 missed
coverage  ▆▆▇▇  latest 79.7%, 4 run(s) — avg 76.8%, min 72.1%, max 79.7%
          +1.4 points since 3h ago; 7 file(s) improved, 2 regressed — worst internal/api.go (-2.1 points)
```

katana measures nothing itself. It asks the runner the project already uses for
coverage and reads what that runner writes:

| Runner | How coverage is turned on |
| --- | --- |
| `go test` | `-coverprofile`, and `-coverpkg` so tests generated into a directory of their own still measure the code they exercise |
| pytest | `--cov`, via the pytest-cov plugin |
| jest, vitest | `--coverage`, writing lcov |
| mocha | run under `nyc` |
| `node --test` | `--experimental-test-coverage`, writing lcov |
| `cargo test` | run under `cargo-llvm-cov`; a project already on `cargo tarpaulin` keeps it |

`-coverpkg=./...` is the default for Go because katana's own layout needs it:
generated tests live in a `tests` directory, and `go test` on its own measures
only the package a test is in, which would report a full suite as covering
nothing. `--coverpkg=""` turns it off.

Any other runner can still be reported on. Run its own coverage tool and point
katana at the result with `--profile`: Go cover profiles, LCOV and Cobertura XML
are all read, whichever tool wrote them.

The raw report is written somewhere temporary and discarded once it has been
read, so no coverage artifact appears at the project root. `--save` keeps it —
that is what `go tool cover -html=coverage.out` and the coverage viewers in CI
want. The compact local history described below is retained either way.

Every report that katana reads is also recorded as compact totals in
`.katana/coverage-history.json`: timestamp, source format, project and per-file
statement counts, plus command, duration and exit status when katana ran the
suite. Imported `--profile` reports are marked as imports. Raw profiles are not
duplicated into history; they remain opt-in through `--save`.

Coverage history retains every compact observation. Its chart and statistics
exclude reports that measured no statements, while still counting those
attempts. With two measurable observations, katana shows the overall change,
how many comparable files improved or regressed, and the worst regression.
`katana status` and the TUI show the same trend without rerunning coverage.

A failing suite still covered whatever it covered: the table is printed, the
failure is called out under it, and katana exits with the suite's own exit code.

### status

```sh
katana status
katana status --tests                     # name the test cases behind each behavior
katana status --file behaviors/cart.md    # one behavior (repeatable)
katana status --strict                    # exit non-zero when anything is out of date
```

```
tracker  .katana/tracker.json (v1, 2 entry(ies), updated 3h ago)
last run  20m ago, failed (exit 1) — 6 of 7 case(s) passed, 1 failed, 0 skipped
history   ▇█████▆█  8 run(s), 2d ago to 20m ago
totals    64 run(s) since 9d ago — 57 passed, 7 failed, 6m12s in the runner
          431 case outcome(s) recorded: 402 passed, 21 failed, 8 skipped
coverage  ▆▆▇▇  latest 79.7%, 4 run(s) — avg 76.8%, min 72.1%, max 79.7%
          +1.4 points since 3h ago; 7 file(s) improved, 2 regressed

┌──────────────────┬──────────────────────┬───────────────────────┬───────┬────────┬────────────┬───────────┬───────────────────────┐
│ STATUS           │ BEHAVIOR             │ TESTS                 │ CASES │ PASSED │ RECENT     │ GENERATED │ STACK                 │
├──────────────────┼──────────────────────┼───────────────────────┼───────┼────────┼────────────┼───────────┼───────────────────────┤
│ up to date       │ behaviors/cart.md    │ tests/cart_test.go    │     5 │    4/5 │ ███▆███▆   │ 3h ago    │ go/go-test via claude │
│ behavior changed │ behaviors/example.md │ tests/example_test.go │     2 │    2/2 │ ████████   │ 6d ago    │ go/go-test via claude │
└──────────────────┴──────────────────────┴───────────────────────┴───────┴────────┴────────────┴───────────┴───────────────────────┘

2 behavior(s), 1 out of date, 7 test case(s) mapped, 6 of 7 passed in the last run
```

The table is the tracker read back: what each behavior is mapped to, how many
test cases came out of it, how many of them passed, when it was generated, and
whether any of it still holds. `CASES` and `GENERATED` are what the last
generation recorded, so a behavior katana has never generated shows `-` in both.

`RECENT` is one column per run from `.katana/history.json`, oldest on the left:
full height for a run in which every one of that behavior's cases passed, red
and shorter for one where some did not. A run that said nothing about a behavior
— a targeted run of another one — is not plotted in its row at all. The `history`
line above the table is the same chart for the suite as a whole.

The `coverage` line comes from `.katana/coverage-history.json`. It shows every
recorded coverage observation in its totals and the recent measurable values in
its sparkline, followed by average, minimum, maximum, and latest change. Empty
or failed report attempts are counted but never plotted as zero percent.

The `totals` line is every run this project has recorded, not only the ones still
in the file: the history keeps the last fifty runs, so its count stops climbing
long before the suite does. It counts targeted runs too — a run of one behavior
is still a run — and the case outcomes are summed across runs, so a suite of ten
cases run twenty times has counted two hundred of them.

`PASSED` is how those cases fared in the last `katana run`, which every run
records to `.katana/results.json`. status never runs the suite itself, so the
count is only as current as that run — the `last run` line says how old it is. A
case the run did not cover counts as neither passed nor failed, and is reported
separately:

```
2 behavior(s), 0 out of date, 7 test case(s) mapped, 2 of 7 passed in the last run (5 case(s) it did not cover)
```

`--tests` names the cases themselves, marked with what each one did: `✓` passed,
`✗` failed, `○` skipped, `•` not in the last run.

```
behaviors/cart.md → tests/cart_test.go (5 case(s), 4 of 5 passed)
  ✓ TestValidDiscountReducesTotal
  ✗ TestDiscountAppliesBeforeShipping
  …
```

A behavior deleted from `katana.yaml` leaves its tracker entry behind until the
next `katana generate` prunes it. Those are listed after the table, so a mapping
that outlived its specification is visible before then:

```
1 tracker entry(ies) no longer in katana.yaml:
  behaviors/old.md → tests/old_test.go (3 case(s), generated 2026-01-14)
run `katana generate` to prune them
```

### tui

```sh
katana                  # in a terminal, inside a project
katana tui
katana tui --snapshot   # print one frame and exit, for a log or a pipe
```

The full-screen view is `katana status` you can move around in, and run things
from. The list is every behavior with its state, its cases, how many of them
passed, and its recent runs; `enter` opens one.

```
 katana  checkout-service          4 behavior(s) · 1 out of date · last run 4m ago passed 9/10

 ┌──────────────────┬───────────────────────┬───────┬────────┬─────────────┬───────────┐
 │ STATUS           │ BEHAVIOR              │ CASES │ PASSED │ RECENT      │ GENERATED │
 ├──────────────────┼───────────────────────┼───────┼────────┼─────────────┼───────────┤
 │ up to date       │ behaviors/checkout.md │     2 │    2/2 │ ██████████  │ 2h ago    │
 │ behavior changed │ behaviors/login.md    │     3 │    2/3 │ ███▆▆███▆█  │ 5h ago    │
 └──────────────────┴───────────────────────┴───────┴────────┴─────────────┴───────────┘

   history   ████████▇█  10 of 64 run(s), oldest shown 1d ago
   coverage  ▆▆▇▇  latest 79.7% · +1.4 pts · 4 run(s) · avg 76.8% · 72.1–79.7%

  ↑↓ select · enter open · r run · a run all · o output · u reload · ? help · q quit
```

Opening a behavior shows the test cases it owns — with how each one last fared
and how long ago that was — and a bar per past run, newest first, of how many of
its cases passed in each:

```
   history  — one row per run, newest first
     3h ago       ██████████████████████████████████      3/3  ✓ 5.43s
     6h ago       ██████████████████████▓▓▓▓▓▓▓▓▓▓▓░      2/3  ✗ 5.30s
     9h ago       ██████████████████████████████████      3/3  ✓ 5.16s
```

`n` and `p` step to the next and previous behavior from there, without going
back to the list.

`r` runs the selected behavior's tests and `a` runs the whole suite, streaming
the runner's output as it arrives; `x` stops a run in flight. A run started here
is recorded exactly as `katana run` records one — the results file, the history
behind the charts — and the list is refreshed from it the moment it finishes, so
what is on screen is what the next `katana status` would say.

Narrowing a run to one behavior needs a runner katana knows how to narrow, as
`--behavior` does; anything else runs the whole suite and says so in the message
line.

`katana` on its own opens this view when it is run in a terminal inside a
project. Anywhere else — a pipe, a script, a directory with no `katana.yaml` —
it prints the usage it always has.

## How staleness is decided

katana hashes both the behavior file and its generated output and compares them
against `.katana/tracker.json`.

| Status | Meaning | Regenerated by default |
| --- | --- | --- |
| `up to date` | Behavior and output both unchanged | — |
| `new` | Never generated, and no test file is there | yes |
| `behavior changed` | The specification changed | yes |
| `output missing` | The generated file is gone | yes |
| `config changed` | Language, framework, harness, or output path changed | yes |
| `output edited by hand` | The generated file was edited since generation | **no** — needs `--force` |
| `output not tracked` | Tests are already there for a behavior the tracker has no entry for | **no** — needs `--force` |

Once a behavior's tests exist and its markdown has not changed since they were
generated, `katana generate` leaves it alone — no agent runs, nothing is
rewritten. Only `--force` regenerates it.

The last two rows are the point of the tracker: katana will not silently discard
a test file it did not write. Whether you edited it by hand or it arrived
without a tracker entry — an older katana, a teammate's run, a file you wrote
yourself — it is reported and skipped until you ask for it to be overwritten.

When the specification *and* the output have both changed, the specification
wins — regeneration is what you asked for by editing the spec. The generated
file is updated in place rather than rewritten from scratch, so unrelated
helpers, fixtures, and imports survive.

Commit `.katana/tracker.json`. It is what lets a teammate's checkout know which
tests are already current.

### What the tracker records

Every successful generation updates that behavior's entry, including an index of
the test cases the generated file declares — so the tracker answers *which tests
came out of this behavior*, without running the suite.

```json
"behaviors/checkout.md": {
  "source_hash": "2abe69f4…",
  "output": "tests/checkout_test.go",
  "output_hash": "8323698b…",
  "tests": ["TestAppliesDiscount", "TestRejectsExpiredCode"],
  "test_count": 2,
  "language": "go",
  "framework": "go-test",
  "harness": "claude",
  "generated_at": "2026-08-10T12:09:39Z"
}
```

The index is read syntactically, per language, and covers the conventions each
framework declares tests with — `func TestX`, `def test_x`, `it("…")`, `@Test`,
`#[test]`, `[Fact]`. A case katana cannot see is missing from the index and
nothing more: staleness is decided by the two hashes alone, so an empty index
never makes a behavior look out of date. `katana generate --verbose` lists the
same cases as they are written.

## Configuration

`katana.yaml` sits at the project root; katana finds it by walking up from the
current directory, the way git finds `.git`. Unknown keys are an error.

```yaml
version: 1

harness:
  name: claude              # claude | codex | opencode | pi | hermes
  # command: claude         # executable to run
  # args: ["-p", "--permission-mode", "auto"]  # placed before the prompt
  # prompt: stdin           # how the prompt is delivered: stdin | arg
  # model: ""               # passed through with model_flag when set
  # model_flag: --model
  # timeout: 10m            # bound on a single generation
  # jobs: 4                 # behaviors generated at once; 1 is sequential
  # env:
  #   KATANA: "1"

defaults:
  language: go
  framework: go-test
  output_dir: tests
  output_template: "{snake}_test.go"

test:
  command: go test ./...
  # dir: .                  # defaults to the project root

behaviors:
  - path: behaviors          # recursive: every .md under behaviors/

  - path: behaviors/billing.md
    output: tests/billing_contract_test.py
    language: python
    framework: pytest
    harness: codex          # use a different agent for this one
    instructions: |
      Stub the payment gateway; never call it for real.
```

A behavior `path` may be a single file, a directory, or a glob. A directory is
searched recursively for `.md` files, so behaviors can be grouped into
subfolders; hidden directories are skipped. Globs may use `**` to span any
number of directories (`behaviors/**/*.md`), while a plain `*` stays within one
path segment. A file matched by two globs is generated once; two behaviors that
would write the same output file is an error, since it would make regeneration
order-dependent.

Behaviors in subfolders keep that structure under `output_dir`: with `path:
behaviors`, `behaviors/billing/limits.md` generates `tests/billing/limits_test.go`,
so it never collides with `behaviors/auth/limits.md`. Nesting is measured from
the part of the path before the first wildcard, so `behaviors/**/*.md` mirrors
the same way. A behavior directly in that directory is not nested. In Go, each
subfolder is its own package — set an explicit `output` if you want everything
in one.

`output_template` supports `{name}` (base name as written), `{snake}`, and
`{Name}` (PascalCase).

## Languages

Built-in conventions — framework, file-name template, and test command — exist
for: `go`, `python`, `javascript`, `typescript`, `java`, `kotlin`, `ruby`,
`rust`, `csharp`, `php`, `swift`. Common aliases (`py`, `ts`, `js`, `golang`,
`dotnet`, …) are normalized.

Any of these are only defaults. Set `framework`, `output_template`, and
`test.command` explicitly to use a language or framework katana does not know.

## Harnesses

```sh
katana harnesses
```

```
NAME      INSTALLED                  INVOCATION                        PROMPT VIA  DESCRIPTION
claude    /Users/you/.local/bin/...  claude -p --permission-mode auto  stdin       Claude Code CLI, non-interactive print mode, auto permissions
codex     no                         codex exec --sandbox workspace-write stdin   Codex CLI, non-interactive exec mode, workspace-write sandbox
hermes    no                         hermes -p                         stdin       hermes CLI, non-interactive prompt mode
opencode  /Users/you/.opencode/...   opencode run                      arg         opencode CLI, single-shot run mode
pi        no                         pi -p                             stdin       pi CLI, non-interactive prompt mode
```

A harness with a permission mode is invoked in one that lets it write, because a
non-interactive agent has nobody to answer a permission prompt: without this the
write is denied and katana fails the behavior with nothing to save. Narrow or
widen that through `harness.args` — `acceptEdits` in place of `auto` limits
Claude Code to file edits and withholds command execution.

These invocations are katana's best-known defaults, not a contract with the
upstream tools. Agent CLIs change their flags, so every field is overridable per
project — a changed flag is a config edit, not a katana release:

```yaml
harness:
  name: codex
  command: codex
  args: ["exec", "--sandbox", "workspace-write"]
  prompt: stdin
```

Setting `harness.command` also lets you point katana at an agent CLI it has
never heard of.

## In CI

```sh
katana status --strict          # behaviors and tests are in sync
katana run --check --save       # the suite passes, and out/ has the report
katana coverage --min 80        # the suite covers enough of the code
```

`katana run` propagates the test suite's own exit code, so CI sees the real
result — including when `--save` wrote a report first. Publish `out/` as a build
artifact to keep a readable record of each run.

`katana coverage --min` fails the build when coverage falls under the percentage
given, and `--save` keeps the runner's own report for whatever reads coverage in
your pipeline.

## How generation works

For each stale behavior, katana builds a prompt containing the behavior
markdown, the target output path, the language and framework, the existing test
file if there is one, and any per-behavior `instructions`. It asks the agent to
write the file itself using its own file tools — that keeps generated code out
of the stdout channel, where agents also emit commentary. If a harness declines
to write the file, katana falls back to recovering the code from stdout.

The prompt tells the agent to read neighbouring tests and the code under test so
the generated tests match your conventions and call the real API, to touch no
file other than the target, and not to run the suite — `katana run` does that.

## Development

```sh
make build   # binary in bin/katana, version stamped from git
make check   # gofmt check, go vet, go test
make help    # all targets
```
