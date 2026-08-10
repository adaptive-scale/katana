# What this part of the product does

This is katana's self-update machinery: it compares the running binary's version against releases published on GitHub, replaces the running binary with a newer one on request, and prints a once-a-day "a newer version exists" hint after a command finishes. The katana command-line tool uses it; the user sees it as the `katana update` command and as an occasional upgrade notice.

## Comparing two version strings

- Comparing two versions returns -1 when the first is older, 0 when they are equal in precedence, and 1 when the first is newer.
- A leading "v" is ignored, so "v1.2.3" and "1.2.3" compare as equal.
- Build metadata after a "+" is ignored, so "1.2.3+build9" and "1.2.3" compare as equal.
- Surrounding whitespace is ignored.
- Missing trailing number segments count as zero, so "1.2" and "1.2.0" compare as equal, and "1.2.1" is newer than "1.2".
- More than three number segments are accepted and compared segment by segment.
- A version whose number segments are not all non-negative whole numbers cannot be parsed.
- A version with no number segments at all — for example a bare "v" or a string that starts with a "-" — cannot be parsed.
- An unparseable version sorts before every parseable version, so an unstamped build is always treated as behind any release.
- Two unparseable versions compare as equal.
- A final release outranks any of its prereleases: "1.2.0" is newer than "1.2.0-rc.1".
- Prerelease identifiers are compared one at a time, left to right, splitting on ".".
- Two numeric prerelease identifiers compare numerically, so "1.0.0-rc.10" is newer than "1.0.0-rc.2".
- A numeric prerelease identifier ranks below a non-numeric one, so "1.0.0-1" is older than "1.0.0-alpha".
- Two non-numeric prerelease identifiers compare as text.
- When one prerelease is a prefix of the other, the one with fewer identifiers is older: "1.0.0-rc" is older than "1.0.0-rc.1".

## Recognising a locally built binary

- An empty version, or a version that is only whitespace, is a development build.
- The exact version "dev" is a development build.
- A version ending in a `git describe` commit suffix — a dash, digits, a dash, "g", then at least four hexadecimal characters — is a development build, for example "v1.2.3-4-gab12cd".
- A version ending in "-dirty" is a development build.
- Any version that cannot be parsed as a version number is a development build.
- A plain published version such as "v1.2.3" or a plain prerelease such as "v1.2.3-rc.1" is not a development build.

## Where releases are read from

- Releases are read from the GitHub repository "adaptive-scale/katana" by default.
- Requests go to "https://api.github.com" unless the environment variable KATANA_GITHUB_API is set, in which case its value is used with surrounding whitespace and any trailing "/" removed.
- The access token is taken from the first non-empty of KATANA_GITHUB_TOKEN, GITHUB_TOKEN, then GH_TOKEN, with surrounding whitespace removed.
- When no token is configured, requests are sent unauthenticated.
- Asking for the latest release reads the repository's "latest release" endpoint; asking by tag reads the release published under that tag.
- Every request announces itself as "katana/<current version>" and asks for GitHub API version "2022-11-28".
- A request carries a bearer authorization header only when a token was found.
- A download or API request that takes longer than 2 minutes is abandoned.

## Errors reported when a release cannot be read

- A "404 Not Found" with no token configured reports "no published release found for adaptive-scale/katana: if the repository is private, set GITHUB_TOKEN".
- A "404 Not Found" with a token configured reports "no published release found for adaptive-scale/katana", without the token advice.
- A "401 Unauthorized" reports "github refused the request (Unauthorized): check GITHUB_TOKEN and its scopes".
- A "403 Forbidden" reports "github refused the request (Forbidden): check GITHUB_TOKEN and its scopes".
- Any other non-200 status reports "unexpected status" together with the status code and the URL that was requested.
- A response whose body is not valid release JSON reports "reading release:" followed by the underlying reason.
- A response that parses but carries an empty tag reports "no published release found".

## Choosing the binary for this platform

- A release is considered outdated when the running version compares as older than the release's tag.
- The preferred asset is the one named exactly "katana_<tag>_<os>_<arch>", where os and arch are the running platform's.
- On Windows the expected asset name ends in ".exe"; on every other platform it has no extension.
- When no exactly named asset exists, any asset whose name starts with "katana_" and ends with the platform suffix is used instead, so a release stamped with a different version string still installs.
- When several assets match the fallback rule, the first one listed in the release is used.
- When no asset matches, the update fails with "no release binary for this platform" followed by the operating system, architecture and release tag.

## Installing an update

- The file replaced is the caller-supplied destination when one is given, otherwise the currently running executable.
- When the running executable is a symbolic link, the link is resolved first so the real binary is rewritten rather than the link replaced.
- If the running executable's location cannot be determined, the update fails with "locating the running binary:" followed by the reason.
- The download is staged in the same directory as the destination, in a temporary file whose name begins with ".katana-update-".
- The staged temporary file is removed when the update finishes, whether it succeeded or failed.
- If the staging file cannot be created because of a permission error, the update fails with "cannot write to <directory>: re-run with sudo, or install elsewhere with the installer's --dir".
- Before downloading, a line "==> downloading katana <tag> (<os>/<arch>)" is written to the progress output.
- Progress output is optional; when no progress writer is supplied, nothing is printed.
- The staged file is made executable before it is put in place.
- On platforms other than Windows the staged file is renamed over the destination in one step, which is safe while the old binary is still running.
- On Windows the existing binary is first moved aside to the destination path plus ".old", with any previous ".old" file removed first.
- On Windows, if putting the new binary in place fails, the moved-aside binary is restored to its original name and the failure is reported.
- On Windows the displaced ".old" file is deleted after a successful swap, and failing to delete it is not treated as an error.
- A failure to put the new binary in place reports "installing to <destination>:" followed by the reason.
- On success the path that was written is returned.

## Fetching asset bytes

- When a token is configured and the asset exposes an API URL, the bytes are fetched through the API URL, which is the only route that works for a private repository.
- When no token is configured, the public browser download URL is used.
- When the browser download URL is missing, the API URL is used regardless of whether a token is configured.
- When the asset has neither URL, the update fails with "release asset <name> has no download url".
- A failed asset request reports "downloading <name>:" followed by the reason.

## Verifying the download

- The download's SHA-256 digest is compared against the release's "checksums.txt" asset.
- When the release publishes no "checksums.txt", the update proceeds and reports "==> release publishes no checksums.txt, skipping checksum verification".
- When the manifest exists but lists no entry for the downloaded asset, the update proceeds and reports "==> checksums.txt lists no entry for <name>, skipping checksum verification".
- When the manifest cannot be downloaded, the update stops and nothing is installed.
- When the recorded digest differs from the downloaded bytes, the update fails with "checksum mismatch for <name>: expected <recorded>, got <computed>" and the binary is not replaced.
- When the digests match, "==> checksum verified" is reported and the install continues.
- Manifest lines are read in the "<digest>  <filename>" form; a leading "*" on the filename, marking binary mode, is ignored.
- A manifest line whose first field is not exactly 64 characters long is ignored.
- A manifest line with no space separating digest from filename is ignored.
- Leading and trailing whitespace around a manifest line and around the filename is ignored.
- A recorded digest is compared in lower case, so an upper-case manifest entry still matches.
- The first manifest line naming the asset wins.

## The background update check

- The check is skipped entirely when the environment variable KATANA_NO_UPDATE_CHECK is set to any non-empty value.
- The check is skipped entirely when the environment variable CI is set to any non-empty value, because the notice would be log noise nobody can act on.
- The check is skipped entirely when the running binary is a development build, so a locally built binary is never offered a downgrade.
- When a check is skipped, no notice is ever printed for that run.
- A previously cached release tag is adopted immediately, so news from an earlier check is repeated on every run until the user upgrades.
- A network request is made only when the last recorded check is more than 24 hours old.
- The background request is abandoned after 5 seconds.
- When the request fails or times out, nothing is recorded, no notice is printed, and the next run tries again.
- When the request succeeds, the release tag and its page URL are remembered and the cache is stamped with the current time.

## Printing the notice

- When a notice is requested, an in-flight check is waited for at most 1 second before giving up.
- No notice is printed when no release tag is known.
- No notice is printed when the known release is the same as, or older than, the running version.
- Otherwise the notice reads "katana <latest> is available (you have <current>). Run `katana update` to install it.", preceded by a blank line.
- The release page URL is printed on a following line only when one is known; a tag recovered from the cache has no URL, so only the first line appears.
- Requesting a notice from a notifier that was never started prints nothing.

## The check cache

- The cache lives in a file named "update.json" inside the katana cache directory.
- The cache directory is the value of KATANA_CACHE_DIR when that variable is set to a non-empty value.
- Otherwise the cache directory is a "katana" folder inside the operating system's per-user cache directory.
- The cache records the time of the last check and the latest tag seen.
- A missing, unreadable or malformed cache file is treated as though no check has ever run, which triggers a fresh check.
- The cache directory is created if needed, and the cache file is written readable by everyone and writable by its owner.
- A cache that cannot be written is not an error; it only means katana asks GitHub again on the next run.
