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
go install github.com/adaptive-scale/katana@latest
```

Or build from a checkout:

```sh
go build -o katana .
```

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

Then the loop is: edit a behavior file, run `katana generate`, run `katana run`.

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
| `katana init` | Create `katana.yaml`, `.katana/`, and a sample behavior |
| `katana generate` | Generate tests for behaviors that changed since the last run |
| `katana run` | Run the test command from `katana.yaml` |
| `katana status` | Show which behaviors are out of date |
| `katana harnesses` | List the supported agent CLIs and whether they are installed |
| `katana version` | Print the katana version |

Run `katana <command> --help` for a command's flags.

### generate

```sh
katana generate                          # only what changed
katana generate --dry-run                # report what would run, run nothing
katana generate --file behaviors/cart.md # one behavior (repeatable)
katana generate --force                  # regenerate everything
katana generate --verbose                # narrate each generation
```

The tracker is saved after each behavior succeeds, so an interrupted run keeps
the work already done. Behaviors deleted from the config have their tracker
entries pruned on a full run.

`--verbose` shows what is being generated rather than only that something is:

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
katana run                         # run the suite
katana run --check                 # fail if any behavior is out of date
katana run --save                  # also write an HTML report to out/
katana run -- -run TestCheckout    # arguments after -- are appended
```

`run` warns when a behavior has changed since its tests were generated, so a
green suite is never mistaken for one that covers the current specification.
`--check` turns that warning into a failure — the useful form in CI.

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
only name individual cases in verbose mode, `--save` appends `-v` to those two
commands and says so.

The suite's exit code is still propagated after the report is written, so
`--save` is safe to leave on in CI.

### status

```sh
katana status
katana status --strict   # exit non-zero when anything is out of date
```

```
STATUS  BEHAVIOR              TESTS                  STACK
new     behaviors/example.md  tests/example_test.go  go/go-test via claude

1 behavior(s), 1 out of date
```

## How staleness is decided

katana hashes both the behavior file and its generated output and compares them
against `.katana/tracker.json`.

| Status | Meaning | Regenerated by default |
| --- | --- | --- |
| `up to date` | Behavior and output both unchanged | — |
| `new` | Never generated | yes |
| `behavior changed` | The specification changed | yes |
| `output missing` | The generated file is gone | yes |
| `config changed` | Language, framework, harness, or output path changed | yes |
| `output edited by hand` | The generated file was edited since generation | **no** — needs `--force` |

The last row is the point of the tracker: katana will not silently discard your
edits. A hand-edited test file is reported and skipped until you ask for it to
be overwritten.

When the specification *and* the output have both changed, the specification
wins — regeneration is what you asked for by editing the spec. The generated
file is updated in place rather than rewritten from scratch, so unrelated
helpers, fixtures, and imports survive.

Commit `.katana/tracker.json`. It is what lets a teammate's checkout know which
tests are already current.

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
codex     no                         codex exec                        arg         Codex CLI, non-interactive exec mode
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
  args: ["exec", "--full-auto"]
  prompt: arg
```

Setting `harness.command` also lets you point katana at an agent CLI it has
never heard of.

## In CI

```sh
katana status --strict          # behaviors and tests are in sync
katana run --check --save       # the suite passes, and out/ has the report
```

`katana run` propagates the test suite's own exit code, so CI sees the real
result — including when `--save` wrote a report first. Publish `out/` as a build
artifact to keep a readable record of each run.

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
