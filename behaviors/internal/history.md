# What this part of the product does

This component keeps a project's recent test-command runs and cumulative run totals in local history. Other parts of the product use it to inspect overall run health and the history of individual behaviors.

## Recording and retaining runs

- Recording a run converts its timestamp to UTC before storing it.
- A recorded run increases the cumulative run count, passed-run count when its exit code is zero, or failed-run count otherwise.
- A recorded run adds its reported pass, fail, skip, and duration values to the cumulative totals.
- Cumulative first and last run timestamps are updated from the recorded run's timestamp.
- The recent-run list keeps at most 50 runs.
- When adding a run would exceed 50 recent runs, the oldest runs are removed from that list.
- Cumulative totals continue to include runs removed from the recent-run list.
- A run reports success only when its exit code is zero.
- A run's reported case total is the sum of its pass, fail, and skip counts.
- A run's pass rate is its pass count divided by its reported case total, or zero when that total is zero.

## Loading and saving history

- Loading a project with no history file returns a new empty history at version 1 without an error.
- Loading a history file sorts its runs from oldest to newest by timestamp, preserving the original order when timestamps are equal.
- A history file containing invalid JSON returns an error beginning with `parsing <history path>:` and tells the caller to delete it so a new history can start.
- A history file with a version other than 1 returns an error stating the file's version and that this katana understands version 1.
- Loading an older history with runs but no cumulative totals rebuilds totals from the runs still present in the recent-run window.
- Loading an older history with existing cumulative runs, or with no runs, does not rebuild its totals.
- Saving sets the history version to 1 and writes the history beneath the project's `.katana` directory as `history.json`.
- Saving creates the `.katana` directory when it does not exist.
- Saving writes a newline-terminated, indented JSON document.
- Saving replaces the history file only after the complete temporary file has been written and closed.
- A single-step record operation starts with the loaded history, adds the run, and saves it.
- If a history cannot be loaded during a single-step record operation, recording starts a new version-1 history instead of returning the load error.

## Querying behavior and totals

- A behavior's known-case count is the sum of its pass, fail, and skip counts.
- A behavior's total-case count is its known-case count plus its unknown count.
- A behavior's pass rate returns no rate when its known-case count is zero; otherwise it is its pass count divided by its known-case count.
- Looking up a behavior in a run returns the first recorded behavior with the requested source, or reports that none was found.
- The total number of cases across cumulative history is the sum of cumulative pass, fail, and skip counts.
- The cumulative pass rate returns no rate when no runs have been recorded; otherwise it is the passed-run count divided by the cumulative run count.
- The cumulative duration is the cumulative millisecond total interpreted as a duration in milliseconds.
- Requesting recent runs with a non-positive count, or with a count at least as large as the available list, returns the available run list.
- Requesting a positive count smaller than the available recent-run list returns that many newest runs in oldest-to-newest order.
- Behavior history includes only runs that recorded the requested behavior with at least one known case.
- Behavior history excludes runs that did not cover the requested behavior or reported no known cases for it.
- Requesting a non-positive behavior-history count returns all matching runs.
- Requesting a positive behavior-history count smaller than the matching list returns that many newest matching runs in oldest-to-newest order.
