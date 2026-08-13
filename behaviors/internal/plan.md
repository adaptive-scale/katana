# What this part of the product does

This part compares configured behaviors with the tracker and their generated test outputs. It supplies the status used by commands and the terminal UI, either for every behavior or for selected behavior files.

## Building a behavior plan

- The plan first resolves the configured behaviors; if resolution returns an error, the plan returns that error and no items.
- With no selected files, the plan includes every resolved behavior.
- With selected files, the plan includes only behaviors whose normalized project-relative source path matches a selected value.
- Each included behavior is checked by hashing its source file and output file.
- If hashing either file returns an error, the plan returns that error and no completed result.
- Each included item reports its resolved behavior, source hash, output hash, and classified tracker status.
- A behavior is stale whenever its status is anything other than `up-to-date`.
- A behavior's stack description is its language, framework, and harness joined as `language/framework via harness`.

## Selecting and naming behavior files

- An absolute selected path is converted to a path relative to the project root when that conversion succeeds.
- Relative and successfully converted absolute paths are cleaned and rendered with forward slashes.
- If converting an absolute path relative to the project root fails, the original absolute path is cleaned and rendered with forward slashes.
- Repeated selected paths have the same effect as one selected path.
- An empty selected path is cleaned and rendered as `.` before matching.

## Classifying tracker state

- A behavior with no tracker entry is `output-untracked` when its output file exists, and is `new` when its output file is missing.
- A behavior whose source hash differs from the tracker entry is `behavior-changed`.
- A behavior whose tracked output path, language, framework, or harness differs from its resolved configuration is `config-changed`.
- When the source and tracked configuration are unchanged, a missing output file is `output-missing`.
- When the source and tracked configuration are unchanged, an existing output is `output-modified` if the tracker stores an output hash and that hash differs from the current output hash.
- When the source and tracked configuration are unchanged, an existing output is `up-to-date` if no stored output hash differs from its current hash.
- A changed source is classified as `behavior-changed` before a modified output is considered.
- If the tracker has no stored output hash, an existing output is not classified as modified solely because its current hash cannot be compared.
