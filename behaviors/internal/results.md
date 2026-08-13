# What this part of the product does

It records the outcomes from the most recent test run so status information can be read without running the suite again. The record is stored per project on the local machine and can represent either a whole-suite run or a targeted behavior run.

## Recording and persistence

- A newly recorded run has schema version `1`, stores its run time in UTC, preserves the command, exit code, and whether results were available per case, and records each reported case with its suite, name, status, run time, and blocked flag.
- Saving a record always writes schema version `1` and creates or updates `.katana/results.json` as indented JSON terminated by a newline.
- Loading a missing results file returns an empty, unrecorded record with version `1` and no error.
- Loading a malformed results file returns an error beginning with `parsing <path>:` and ending with ` (delete it; the next \`katana run\` writes it again)`.
- Loading a results file with a version other than `1` returns an error stating `results <path> have version <found>, this katana understands 1`.
- A loaded case with no per-case timestamp receives the record's overall run time as its timestamp.
- A record is considered recorded only when its overall run time is non-zero.

## Combining targeted runs

- Inheriting from a previous record does nothing when either record is absent, unrecorded, or does not contain per-case results.
- In a per-case run, outcomes not reported by that run are carried forward from the previous per-case record, retaining their original timestamps.
- A case reported by the current run is not duplicated when an earlier case has the same name after trimming surrounding whitespace and replacing spaces with underscores.
- A blocked suite is not inherited into the current record, and blocked case markers from the previous record are never inherited.
- Blocked suites are listed once each, in the order their blocked cases first appear.
- A run with no per-case results is not mixed with individual case outcomes from an earlier run.

## Querying outcomes and times

- A case outcome can be queried only from a recorded per-case record; an absent case returns no outcome.
- A case name matches after surrounding whitespace is trimmed and spaces are replaced with underscores.
- A subtest contributes its outcome to its own name and to every slash-separated ancestor name; for example, `TestX/sub/deep` is also counted for `TestX/sub` and `TestX`.
- For one queried name, any failing recorded case produces `fail`; otherwise any passing case produces `pass`; a name whose recorded cases only skip produces `skip`.
- The timestamp for a queried case is the newest timestamp among the case and its subtests; if all timestamps are zero, the record's overall run time is returned.
- The last-run time for a list of names is the newest available timestamp among those names; an empty list or no matching cases returns no time.

## Tallying and run status

- The overall pass/fail result is determined by the recorded exit code being zero, even when individual cases report another outcome.
- Counts for the last run include only cases whose timestamp is zero or equals the record's overall run time; inherited cases are excluded from those counts.
- A tally counts failing cases as `Fail`, skipped cases as `Skip`, and every other known status as `Pass`.
- A tally over requested names counts a missing name as `Unknown`; `Total` is the sum of pass, fail, skip, and unknown counts, while `Known` excludes unknown cases.
- Adding one tally to another sums each of their pass, fail, skip, and unknown counts.
- The number of inherited outcomes is the number of recorded cases whose timestamp does not match the overall run time, except that a zero case timestamp is treated as belonging to the overall run.
