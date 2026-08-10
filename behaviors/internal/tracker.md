# What this part of the product does

Katana keeps a record of what it generated, from which behavior file, and with what settings, so a later run can tell what has changed and regenerate only that. This is the state store behind that decision: it loads and saves the record for a project, hashes behavior files and generated files so they can be compared, and names the reasons a behavior is or is not out of date.

## Where the record lives

- The record for a project is a single file named `tracker.json` inside a `.katana` directory under the project root.
- Loading a project whose tracker file does not exist yields an empty record rather than an error, so a first run needs no setup.
- The record carries a schema version, and the version this katana writes and understands is `1`.

## What is recorded per behavior

- Records are keyed by the behavior source path, so recording twice for the same source replaces the earlier record instead of adding a second one.
- Each record holds the behavior source path, a hash of the behavior file, the generated output path, a hash of the generated file, the language, the framework, the harness, and the time generation happened.
- Each record may also carry the katana version that produced it; when that is empty it is left out of the saved file entirely.
- Each record carries an index of the test cases the generated file declares, in the order they appear in that file, so a reader can see which tests came out of a behavior without running the suite.
- An empty test index means katana could not read cases out of the generated file. It never means the behavior is out of date — staleness is decided only by the behavior hash and the output hash.
- The test count stored in the file is always the number of entries in the test index; a count supplied by the caller is replaced with the true length when the record is stored.
- When the test index is empty, both the index and the count are left out of the saved file.

## Reading and rejecting an existing record

- A tracker file that is not valid JSON is rejected with an error naming the file, the parse failure, and the advice `(delete it to start over)`.
- A tracker file whose version is anything other than `1` is rejected with an error of the form `tracker <path> has version <found>, this katana understands 1`; the record is not used.
- A tracker file that parses but contains no entries section loads as a record with no entries, not an error.
- Looking up a behavior source that was never recorded reports that it is absent, distinct from finding an empty record.

## Dropping records that no longer apply

- Pruning takes the set of behavior sources that are still configured and removes every recorded entry whose source is not in that set.
- Pruning reports the sources it removed, sorted alphabetically.
- Pruning against an empty set of configured sources removes every entry.
- Pruning that removes nothing reports an empty list and leaves the record unchanged.

## Saving

- Saving does nothing and reports success when nothing has been recorded or pruned since the record was loaded, so an unchanged project never has its tracker file rewritten or created.
- Recording an entry or pruning at least one entry marks the record as needing a save.
- A successful save clears the pending-change mark, so an immediately repeated save writes nothing.
- Saving stamps the record with the current time in UTC and with schema version `1`, overwriting whatever was loaded.
- The `.katana` directory is created if it does not exist.
- The file is written as JSON indented with two spaces and ends with a newline.
- The save is atomic: content is written to a temporary file in the same directory and then renamed into place, so an interrupted run cannot leave a half-written tracker file. The temporary file is removed whether or not the save succeeds.

## Hashing behavior and output files

- Content already held in memory is hashed as SHA-256, rendered as lowercase hexadecimal, so a caller that just read a file need not read it again.
- Hashing a file by path reads it and returns the same lowercase hexadecimal SHA-256.
- A file that does not exist hashes to the empty string and reports no error, so a caller can compare hashes directly without a separate existence check.
- A file that exists but is empty hashes to the SHA-256 of empty content, which is not the empty string — so "missing" and "empty" are distinguishable by hash.
- Any other failure to open or read the file is reported as an error with no hash.

## Why a behavior is or is not out of date

- The reasons a behavior can be in are: unchanged, never generated, behavior markdown changed since generation, generated file gone, generated file edited by hand, and generation settings changed.
- Each reason has a fixed wording for command-line output: `up to date`, `new`, `behavior changed`, `output missing`, `output edited by hand`, and `config changed`.
- Settings changing means the language, framework or harness differ from what was recorded.
- Any reason outside the six listed renders as `unknown`.
- By default, regeneration is called for when a behavior is new, when the behavior markdown changed, when the generated file is missing, or when the settings changed.
- A generated file that was edited by hand deliberately does not call for regeneration by default: katana will not silently discard hand-written edits, so that case requires forcing.
- A behavior that is unchanged does not call for regeneration.
