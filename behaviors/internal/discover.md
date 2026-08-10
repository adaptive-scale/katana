# What this part of the product does

Discovery reads an existing codebase and writes back the behavior it already implements as behavior markdown, so a project with untested code has specifications to generate tests from. It scans source files into units, asks a coding-agent harness to describe each unit, and makes sure a behavior file ends up on disk for it.

## Choosing a grouping

- A scan with no grouping set behaves as if the "dir" grouping was requested.
- The "dir" grouping produces one unit per source directory, holding every source file found directly in that directory.
- The "file" grouping produces one unit per source file, each holding exactly that one file.
- A grouping that is neither "dir" nor "file" is rejected with `unknown grouping "x"; use dir or file`, and no scanning happens.
- The accepted grouping names are reported as the list "dir", "file".

## Choosing what counts as source

- A scan for a language katana has no file extensions for is rejected with `katana does not know which files are <language> source; discovery needs one of: <the known languages>`, listing the languages it does know.
- A scan with no paths given walks the whole project from its root.
- A scan with paths given walks only those project-relative files or subtrees.
- A requested path that cannot be inspected on disk is reported as an error naming that path in quotes followed by the underlying reason.
- A requested path that names a single file is accepted only if that file is a source file for the chosen language; otherwise the scan fails with `"x" is not a <language> source file`.
- A file named outright on the command line is included even when it is test code and even when it looks generated.
- A scan that finds no source files at all returns no units and no error.

## What a directory walk skips

- Any directory whose name begins with "." is not descended into, which covers the repository and katana state directories without naming them.
- Directories named vendor, node_modules, bower_components, third_party, thirdparty, dist, build, out, target, bin, obj, coverage, `__pycache__`, site-packages, venv, `_build`, pods, deriveddata, or generated are not descended into, matched without regard to letter case.
- The katana configuration directory and the configured behavior directory are not descended into, so existing specifications are never treated as source.
- A directory that was itself asked for on the command line is always descended into, whatever its name, even if that name is on the skip list.
- An excluded entry matches either a directory's plain name at any depth, or its exact project-relative path, or any directory beneath that path.
- Exclusion entries that are empty or that reduce to "." are ignored rather than excluding everything.
- Test files are left out of a directory walk unless tests are explicitly included.

## What counts as generated

- A file found by walking a directory is left out when its name ends in any of `.min.js`, `.min.ts`, `.pb.go`, `.pb.gw.go`, `_pb2.py`, `_pb2_grpc.py`, `.pb.cc`, `.pb.php`, `_generated.go`, `.gen.go`, `.generated.cs`, `.designer.cs`, `.g.cs`, `.d.ts`, or `.g.dart`, compared without regard to letter case.
- A file whose first 1024 bytes contain "do not edit" or "@generated", in any letter case, is left out as generated.
- A file that cannot be opened to check its header is not treated as generated on that basis alone.

## The units a scan produces

- A unit reports the directory or file it came from as its name, project-relative.
- A directory-grouped unit lists its source files in sorted order and reports the total size of those files.
- A file-grouped unit reports the size of its single file.
- A unit for files at the project root records its directory as ".".
- Units come back sorted by the behavior file each one writes.

## Where each unit's specification is written

- Behavior files mirror the source tree: a source directory becomes a same-named markdown file under the behavior directory, so a package one level down keeps that same one-level position.
- Under the "file" grouping the behavior file keeps the source file's directory and base name with its extension replaced by ".md".
- A directory-grouped unit for files at the project root is named after the project root directory instead of after ".".
- When the project root has no usable directory name, the root unit's behavior file is named "root".
- A behavior directory that is empty, or that reduces to "/", is treated as "behaviors".
- When two units would write the same behavior file, each one that can be distinguished gets a suffix appended to its name after an underscore, so neither silently overwrites the other.
- Under the "file" grouping the distinguishing suffix is the source file's extension without the leading dot, so two same-named files with different extensions get different behavior files.
- Under the "dir" grouping only a root unit can be distinguished, and its suffix is "root"; other colliding directory units keep the same output path.

## Asking the harness for one unit's specification

- Before running, the current contents of the unit's behavior file are read, and an absent file counts as empty rather than an error.
- The finished prompt is handed to the caller's prompt observer, when one is set, before the harness is started, so a verbose run can show what is being asked while the work is still in progress.
- A harness that fails to run causes the discovery of that unit to fail with the harness's own error.
- After the harness finishes, the behavior file on disk is read again to decide what happened.

## What the prompt asks for

- The prompt names the language and lists every source file in the unit, and states that those files are the only source of what is written.
- The prompt names the exact project-relative output path, tells the harness to create any parent directories, and tells it to write the file itself with its file tools rather than printing the specification.
- The prompt allows printing the whole file in a single fenced code block as the entire reply, but only when writing the file is impossible because no file tool exists or a permission check denies the write, and tells the harness not to stop or ask for access in that case.
- The prompt shows the required markdown shape: a title, a sentence or two of context, and one section per group of related behavior with bullets that each state one checkable fact.
- The prompt requires observable behavior only, one checkable fact per bullet, real error messages and literal values quoted, edge cases covered, and no code, code fences, function signatures or file paths in the result.
- The prompt forbids modifying any file other than the named output and forbids writing tests.
- When a specification already exists at the output path, it is included in the prompt with instructions to correct what the code contradicts, add what it has gained, remove what it no longer has, and otherwise keep existing wording, ordering and structure because it may have been hand-edited.
- When the existing specification is empty or only whitespace, no existing-specification section appears in the prompt.
- Extra project instructions, when supplied and not just whitespace, appear as their own section after the requirements.
- The prompt tells the harness that files holding no product behavior — generated code, plain data declarations, constants, or a package that only wires other packages together — should not be written at all, and that the reply should instead be a single line beginning with "SKIP:" followed by the reason in a few words.

## How the harness's reply is interpreted

- When the behavior file is unchanged from before the run and the reply reports a skip, the unit is reported as skipped with that reason and nothing is written.
- A skip is only recognised when the marker "SKIP:" starts a line of its own, after leading whitespace and any leading "#", "*", "-", ">" or backtick characters are ignored; a skip mentioned inside a paragraph is treated as commentary.
- The skip marker is matched without regard to letter case.
- Trailing emphasis characters — asterisks, backticks and underscores — are stripped from the end of a skip reason.
- A skip line with no reason after the marker is reported with the reason "no behavior to specify".
- A skip reason longer than 200 characters is cut to 200 characters with "…" appended.
- When the behavior file changed during the run, a skip reported in the reply is ignored and the written file wins.
- When the behavior file is non-empty after the run, the unit is reported as written by the harness, with the file's size in bytes.
- When the file is non-empty and byte-identical to what was there before, the unit is additionally reported as unchanged, meaning the harness read the code and found nothing to correct.
- Every outcome carries the harness's own reply, trimmed of surrounding whitespace, for diagnostics.

## Recovering a specification from the harness's reply

- When no behavior file exists after the run, the specification is recovered from the harness's printed reply and written to the output path, and the unit is reported as coming from the reply rather than from a file the harness wrote.
- Recovery prefers the largest fenced code block in the reply, since that is the shape the prompt asked for.
- Unfenced output is used only when the whole reply itself reads as a specification.
- Text only counts as a specification when it contains at least one line starting with "#" and at least two bullet lines starting with "- " or "* "; anything else is rejected, so an agent's chatter is not written into the behavior file.
- A recovered specification is written with surrounding whitespace trimmed and a single trailing newline.
- Recovery creates any missing parent directories of the output path.

## When nothing usable comes back

- When the harness neither leaves a behavior file nor prints anything recognisable as a specification, discovery of that unit fails with `harness "<name>" did not write <output path> and printed no specification; harness said: <summary>`.
- The summary quotes the harness's standard output, or its error output when standard output is empty, or "(no output)" when both are empty.
- The summary is flattened to a single line and cut to 400 characters with "..." appended when longer.
- When a permission hint is available for that harness's output, it is appended to the failure on its own indented line.
