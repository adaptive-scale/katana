# What this part of the product does

Turns one written behavior specification into a test file by asking a configured harness (an AI coding agent) to write it, then confirming the file actually landed on disk. The `katana generate` command uses it, once per behavior, and reports back what each generation produced.

## Producing a test file for one behavior

- A generation is given a behavior specification, the project-relative path the tests belong at, a language, and a test framework; it produces the test file at that path.
- The output path is interpreted relative to the generator's configured project root, with forward slashes in the path treated as directory separators regardless of platform.
- If a file already exists at the output path, its current contents are read before the harness runs and are supplied to the harness as the existing tests to update.
- If no file exists at the output path, the existing-tests content is empty and the harness is asked to create the file fresh.
- When a caller has registered a prompt observer, it receives the fully assembled prompt before the harness is invoked, so `katana generate --verbose` can show the request while the harness is still working.
- If the harness itself fails to run, that failure is reported as-is and no file is written.
- If the existing file at the output path cannot be read for a reason other than "does not exist", generation stops and reports that read error.

## Deciding whether generation succeeded

- If a non-empty file exists at the output path after the harness exits, the result is reported as the harness having written the file itself, along with the file's size in bytes.
- If the file after the run is byte-identical to the file before the run, the result is reported as unchanged — and this counts as success, not failure, because the harness may have judged the existing tests to already satisfy the specification.
- A file that is empty after the harness runs is treated the same as no file at all, and the stdout fallback is attempted.
- Whichever path succeeds, the harness's own reply is retained with the result for diagnostics, with surrounding whitespace trimmed.

## Falling back to the harness's printed output

- When the harness leaves no file behind, the file body is recovered from the harness's standard output and written to the output path; the result is then reported as having come from stdout rather than from the harness writing the file.
- Any parent directories the output path needs are created when katana writes the file itself.
- When recovering from standard output, the largest fenced code block in the output is preferred as the file body, with surrounding whitespace trimmed and a single trailing newline added.
- When the output contains no fenced code block, the whole output is used verbatim only if it looks like source code rather than conversational prose; whitespace is trimmed and a trailing newline added.
- Output that is entirely whitespace is never used as a file body.

## Telling code apart from an agent's reply

- Output of fewer than three lines is never accepted as a file body, because a one-line confirmation is not a file.
- A line counts as code-like when it ends with `{`, `}`, `;` or `:`, when it is exactly `}` or `)`, or when it begins with a tab or four spaces.
- A line also counts as code-like when it starts with one of the recognized source markers: `package `, `import `, `from `, `def `, `func `, `class `, `fn `, `#include`, `using `, `namespace `, `module `, `require `, `@Test`, `describe(`, `it(`, `test(`, `const `, `let `, `var `, `public `, `private `, `internal `, `assert`, `expect(`, `self.`, `#[`.
- Blank lines are ignored entirely when judging whether output is code.
- Unfenced output is accepted as code only when at least three lines are code-like and code-like lines make up at least one third of the non-blank lines.
- A paragraph of prose containing one or two code-like lines is rejected, so the failure surfaces as a clear error rather than English written into a test file.

## Reporting a failed generation

- When the harness neither writes the file nor prints usable code, generation fails with an error naming the harness and the output path, in the form: harness "<name>" did not write <path> and printed no test code; harness said: <summary>.
- The summary in that error is the harness's standard output, or its standard error when standard output is empty, or the literal `(no output)` when both are empty.
- The summary is flattened to a single line by replacing newlines with spaces, and is truncated to 400 characters followed by `...` when longer.
- When a permission-related hint is available for the harness given its output, that hint is appended to the error on its own indented line.

## What the harness is asked to do

- The prompt states the target language and framework and gives the exact output path, instructing the harness to create any parent directories and to write the file with its own file tools rather than printing it.
- The prompt permits printing the file contents as a single fenced code block, with no surrounding prose, only when writing fails because no file tool is available or a permission check denies the write — and tells the harness not to stop or ask for access in that case.
- When existing tests are present, the prompt includes them and asks the harness to update the file in place: change cases the specification changed, add cases it added, remove cases it no longer describes, and preserve unrelated hand-written helpers, fixtures and imports.
- When there are no existing tests, no existing-tests section appears in the prompt at all.
- The prompt always includes the behavior specification's content along with the project-relative path of the behavior file it came from.
- The prompt requires one test per distinct asserted behavior, covering every behavior stated including error and edge cases, with each test named after the behavior it verifies.
- The prompt requires assertions on the described behavior rather than incidental implementation detail, and asks that gaps in the specification be resolved with a reasonable interpretation noted in a brief comment rather than an invented requirement.
- The prompt requires matching the surrounding codebase's conventions by reading a neighbouring test file first, and reading the code under test when it exists in the repository rather than inventing function signatures.
- The prompt requires the file to compile and be runnable by the project's normal test command, forbids modifying any file other than the target test file, and forbids running the test suite.
- Extra project instructions, when supplied, are appended as an additional section with surrounding whitespace trimmed; when they are empty or only whitespace, that section is omitted.
- The prompt closes by asking for a one-line confirmation of the path once the file is written, or the fenced file contents if it could not be written.
