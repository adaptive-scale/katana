# What this part of the product does

Katana reads a project configuration file named `katana.yaml`, fills in the settings the project left unsaid, rejects settings it cannot honour, and expands the configured behaviour sources into a concrete list of behaviour files paired with the test files that will be generated from them. Every katana command that needs to know what to generate, where to write it, or how to run the suite goes through this.

## Locating the configuration file

- Searching from a starting directory returns the path of the first `katana.yaml` found in that directory or any ancestor of it, walking upwards one directory at a time.
- The starting directory is resolved to an absolute path before the walk, so a relative starting point is searched the same as an absolute one.
- When the walk reaches the filesystem root without finding the file, it reports "no katana.yaml found in <start> or any parent directory (run katana init first)" — with the words katana init in backticks — naming the original starting directory rather than the root it stopped at.

## Loading and parsing

- Loading reads the named file; a missing or unreadable file surfaces the underlying read failure unchanged.
- The file is parsed as YAML with unknown fields rejected: a key that is not part of the schema fails the load with `parsing <path>: ` followed by the parse error.
- The recognised top-level sections are `version`, `harness`, `defaults`, `test` and `behaviors`.
- The directory containing the configuration file becomes the project root, and every relative path in the configuration is resolved against that root rather than against the process working directory.
- Defaults are applied before validation, so a value the project omitted is validated as the default it received.

## Defaults applied to an incomplete configuration

- An absent or zero `version` becomes `1`.
- An absent `harness.name` becomes `claude`.
- An absent `defaults.language` becomes `go`.
- An absent `defaults.output_dir` becomes `tests`.
- An absent `defaults.framework` becomes the conventional framework for the effective default language.
- An absent `defaults.output_template` becomes the conventional file-name template for the effective default language.
- An absent `test.command` becomes the conventional test command for the effective default language.
- Because the language default is applied first, a configuration that sets nothing at all ends up with the Go conventions: framework `go-test`, template `{snake}_test.go`, test command `go test ./...`.

## Configuration that is rejected

- Any `version` other than `1` fails with `unsupported config version <n> (this katana understands version 1)`.
- A configuration with an empty or absent `behaviors` list fails with `no behaviors configured in katana.yaml`.
- A behaviour whose `path` is empty or only whitespace fails with `behaviors[<i>]: path is required`, where `<i>` is the zero-based position in the list.
- A behaviour that sets an explicit `output` while its `path` contains any of `*`, `?` or `[` fails with `behaviors[<i>]: output cannot be set for a glob path "<path>"; use output_template or list the files individually`.
- A `harness.prompt` that is neither empty nor `stdin` nor `arg` fails with `harness.prompt must be "stdin" or "arg", got "<value>"`.
- A negative `harness.jobs` fails with `harness.jobs must be zero or positive, got <n>`; zero is accepted.
- An invalid `harness.timeout` is rejected at load time, not deferred to the point of use.

## Generation timeout

- An empty or whitespace-only `harness.timeout` yields a timeout of ten minutes.
- A timeout is written in Go duration syntax, such as `10m` or `90s`.
- A value that cannot be parsed as a duration fails with `harness.timeout "<value>": ` followed by the parse error.
- A duration that parses but is zero or negative fails with `harness.timeout must be positive, got "<value>"`.

## Generation concurrency

- A `harness.jobs` greater than zero is the number of behaviours generated at once.
- A `harness.jobs` of zero, or an absent one, yields `4` concurrent generations.
- A `harness.jobs` of `1` means generation is sequential.

## Where discovered behaviours are written

- The behaviour directory is derived from the first configured behaviour path: the fixed portion before any wildcard, or the containing directory when the path names a single file.
- When that fixed portion ends in a `.md` extension — including a behaviour file that does not exist yet — its parent directory is used instead.
- A fixed portion that is empty, `.` or `/` is skipped and the next configured behaviour path is consulted.
- When no configured behaviour path yields a usable directory, the behaviour directory is `behaviors`.

## Expanding behaviour paths

- A path containing none of `*`, `?` or `[` that names an existing file resolves to exactly that one file.
- A path containing none of `*`, `?` or `[` that names an existing directory resolves to every `.md` file beneath it, searched recursively, so behaviours can be grouped into subfolders.
- The `.md` test is case-insensitive, so a file ending in `.MD` is collected too.
- A non-wildcard path that does not exist fails with `"<path>": ` followed by the underlying stat error.
- A pattern containing `**` matches any number of path segments at that position, including zero.
- In a pattern, a single `*` stays confined to one path segment and does not cross a `/`.
- When the fixed prefix of a `**` pattern names a directory that does not exist, the result is no matches rather than an error.
- Any other wildcard pattern is expanded with ordinary shell-style globbing rooted at the project root.
- Directories are never returned as matches; only files are.
- Directories whose name begins with `.` are skipped during recursive searches, so tool state such as `.git` and `.katana` never becomes a behaviour source.
- Matches are reported as project-relative paths using forward slashes, sorted alphabetically.

## Resolving behaviours into generation work

- Resolution returns one entry per matched behaviour file, each carrying a source path, an output path, a language, a framework, a harness and any per-behaviour instructions.
- Entries are sorted by source path.
- A behaviour path that expands to nothing fails with `behaviors[<i>]: "<path>" matched no files`.
- An expansion failure is reported as `behaviors[<i>]: ` followed by the underlying error.
- A file matched by more than one configured behaviour is generated once; the first configured entry that matched it wins and later ones skip it.
- A behaviour's `language` overrides the default language; when it is empty or only whitespace, the default language is used.
- A behaviour's `harness` overrides `harness.name`; when it is empty or only whitespace, `harness.name` is used.
- A behaviour's `framework` is used as given when set.
- When a behaviour sets no framework and its effective language equals the default language, it inherits `defaults.framework`.
- When a behaviour sets no framework and its effective language differs from the default language, it gets the conventional framework for its own language rather than the configured default, so a per-behaviour language never inherits a mismatched framework.

## Output paths

- A behaviour with an explicit `output` writes to that path, normalised to forward slashes with redundant elements removed.
- A behaviour without an explicit `output` writes under `defaults.output_dir`, at a name produced by rendering the output template against the behaviour's file name.
- The template used is `defaults.output_template`, except when the behaviour's effective language differs from the default language, in which case the conventional template for that language is used instead.
- A behaviour file that sits in a subfolder of its pattern's fixed base keeps that subfolder structure beneath the output directory, so two behaviours with the same file name in different subfolders remain distinct output files.
- A behaviour file that sits directly in the pattern's fixed base gets no extra subfolder.
- Two behaviours that would generate the same output file fail with `behaviors "<first>" and "<second>" both generate "<output>"; give one an explicit output`.

## Languages katana knows

- The built-in languages are `go`, `python`, `javascript`, `typescript`, `java`, `kotlin`, `ruby`, `rust`, `csharp`, `php` and `swift`, and they are listed alphabetically.
- Language names are matched after trimming surrounding whitespace and lowercasing, so `Go` and ` GO ` are the same as `go`.
- `js`, `node` and `nodejs` all mean `javascript`.
- `ts` means `typescript`.
- `py` means `python`.
- `rb` means `ruby`.
- `cs`, `c#`, `.net` and `dotnet` all mean `csharp`.
- `golang` means `go`.
- A name that is neither a known language nor a known alias is left as written and simply matches nothing.

## Per-language conventions

- Go: framework `go-test`, template `{snake}_test.go`, test command `go test ./...`, source extension `.go`.
- Python: framework `pytest`, template `test_{snake}.py`, test command `pytest`, source extension `.py`.
- JavaScript: framework `jest`, template `{snake}.test.js`, test command `npm test`, source extensions `.js`, `.jsx`, `.mjs`, `.cjs`.
- TypeScript: framework `vitest`, template `{snake}.test.ts`, test command `npm test`, source extensions `.ts`, `.tsx`, `.mts`, `.cts`.
- Java: framework `junit5`, template `{Name}Test.java`, test command `mvn test`, source extension `.java`.
- Kotlin: framework `junit5`, template `{Name}Test.kt`, test command `gradle test`, source extension `.kt`.
- Ruby: framework `rspec`, template `{snake}_spec.rb`, test command `bundle exec rspec`, source extension `.rb`.
- Rust: framework `cargo-test`, template `{snake}_test.rs`, test command `cargo test`, source extension `.rs`.
- C#: framework `xunit`, template `{Name}Tests.cs`, test command `dotnet test`, source extension `.cs`.
- PHP: framework `phpunit`, template `{Name}Test.php`, test command `vendor/bin/phpunit`, source extension `.php`.
- Swift: framework `xctest`, template `{Name}Tests.swift`, test command `swift test`, source extension `.swift`.

## Conventions for an unknown language

- An unknown language has no conventional framework; the framework comes back empty.
- An unknown language has no conventional test command; the command comes back empty.
- An unknown language falls back to the output template `{snake}_test.txt`.
- An unknown language reports no source extensions, which is what stops discovery from walking a repository blind.

## Recognising source files

- A path counts as source for a language when its extension matches one of that language's extensions, compared case-insensitively, so `.GO` counts as Go source.
- A path in a language katana has no conventions for is never source.

## Recognising test files

- A path is test code when any of its directory segments — every segment except the final file name — is `test`, `tests`, `spec`, `specs`, `__tests__`, `testdata`, `fixtures` or `e2e`, compared case-insensitively.
- The directory rule applies whatever the file is called, and applies even for a language katana has no conventions for.
- Beyond the directory rule, a file in an unknown language is not test code.
- The name rule strips the file's extension first and then matches prefixes and suffixes against what remains, so `user_test.go` and `UserTest.java` are both test code.
- Name matching is case-sensitive, so `Latest.java` is not test code even though it ends in `test` when lowercased.
- Go test names end in `_test`.
- Python test names begin with `test_` or end in `_test`.
- JavaScript and TypeScript test names end in `.test` or `.spec` once the final extension is removed, so `user.test.js` and `user.spec.ts` are test code.
- Java test names end in `Test`, `Tests` or `IT`.
- Kotlin test names end in `Test`, `Tests` or `Spec`.
- Ruby test names end in `_spec` or `_test`.
- Rust test names end in `_test` or `_tests`.
- C#, PHP and Swift test names end in `Test` or `Tests`.

## Rendering output file names

- A template placeholder `{name}` becomes the behaviour file's base name with its extension removed, exactly as written.
- A template placeholder `{snake}` becomes that name in snake_case.
- A template placeholder `{Name}` becomes that name in PascalCase.
- Text in the template outside the placeholders is kept as written, so `{snake}_test.go` applied to `checkout-flow.md` yields `checkout_flow_test.go`.

## Snake_case conversion

- Hyphens, spaces, dots and underscores all become a single underscore.
- Runs of those separators collapse: no two underscores appear in a row.
- An uppercase letter that follows a lowercase letter or a digit gets an underscore inserted before it, and is lowered.
- A run of consecutive uppercase letters is not split, so `HTTPServer` becomes `httpserver`.
- Leading and trailing underscores are trimmed from the result.
- `Checkout Flow`, `checkout-flow` and `checkoutFlow` all convert to `checkout_flow`.

## PascalCase conversion

- The name is first converted to snake_case, then each underscore-separated part has its first character uppercased and the rest left as it stands.
- The parts are joined with no separator, so `checkout_flow` becomes `CheckoutFlow`.
