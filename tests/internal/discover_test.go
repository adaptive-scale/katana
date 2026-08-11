// This file covers behaviors/internal/discover.md: scanning an existing
// codebase into units, deciding where each unit's specification is written,
// asking a coding-agent harness to write it, and making sense of whatever the
// harness replies with.
//
// Scanning is observed through discover.Scan, and prompt content through
// discover.BuildPrompt. The harness half of the specification is observed
// through discover.Discoverer.Discover driving a real child process: a Runner
// shells out to an executable, so every Discover test re-runs this test binary
// as the fake coding agent (see TestDiscoverHelperProcess) and tells it what to
// write, print and exit with.
//
// Helpers here are named for discovery rather than reusing the ones in the
// other files of this package, so this file stands on its own.

package internal

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/discover"
	"github.com/adaptive-scale/katana/internal/harness"
)

// --- Project fixtures ------------------------------------------------------

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// sourceTree lays out a project in a temp directory and returns its root.
func sourceTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, files)
	return root
}

// namedSourceTree lays a project out in a directory with a chosen name, for the
// rules that name a behavior file after the project root directory.
func namedSourceTree(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, files)
	return root
}

// --- Scanning helpers ------------------------------------------------------

func scanUnits(t *testing.T, opts discover.Options) []discover.Unit {
	t.Helper()
	units, err := discover.Scan(opts)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return units
}

func scanErr(t *testing.T, opts discover.Options) error {
	t.Helper()
	units, err := discover.Scan(opts)
	if err == nil {
		t.Fatalf("Scan succeeded, want an error; units = %+v", units)
	}
	return err
}

// scanGo is the scan most tests want: go source, behavior files under
// "behaviors", the default grouping.
func scanGo(t *testing.T, root string) []discover.Unit {
	t.Helper()
	return scanUnits(t, discover.Options{Root: root, Language: "go", BehaviorDir: "behaviors"})
}

func unitOutputs(units []discover.Unit) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, u.Output)
	}
	return out
}

func unitNames(units []discover.Unit) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, u.Name)
	}
	sort.Strings(out)
	return out
}

// scannedFiles is every source file the scan gathered, across all units.
func scannedFiles(units []discover.Unit) []string {
	var out []string
	for _, u := range units {
		out = append(out, u.Files...)
	}
	sort.Strings(out)
	return out
}

func unitNamed(t *testing.T, units []discover.Unit, name string) discover.Unit {
	t.Helper()
	for _, u := range units {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("no unit named %q; units are %v", name, unitNames(units))
	return discover.Unit{}
}

func hasString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// --- Choosing a grouping ---------------------------------------------------

func TestAScanWithNoGroupingBehavesAsIfDirWasRequested(t *testing.T) {
	files := map[string]string{
		"internal/billing/charge.go": "package billing\n",
		"internal/billing/refund.go": "package billing\n",
	}
	root := sourceTree(t, files)

	unset := scanUnits(t, discover.Options{Root: root, Language: "go", BehaviorDir: "behaviors"})
	dir := scanUnits(t, discover.Options{Root: root, Language: "go", BehaviorDir: "behaviors", Group: discover.Grouping("dir")})

	if !reflect.DeepEqual(unitOutputs(unset), unitOutputs(dir)) {
		t.Errorf("no grouping produced %v, want the dir grouping's %v", unitOutputs(unset), unitOutputs(dir))
	}
	if !reflect.DeepEqual(unitNames(unset), unitNames(dir)) {
		t.Errorf("no grouping produced units %v, want %v", unitNames(unset), unitNames(dir))
	}
}

func TestTheDirGroupingProducesOneUnitPerSourceDirectory(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"internal/billing/charge.go": "package billing\n",
		"internal/billing/refund.go": "package billing\n",
		"internal/config/config.go":  "package config\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Group: discover.Grouping("dir"),
	})

	if want := []string{"internal/billing", "internal/config"}; !reflect.DeepEqual(unitNames(units), want) {
		t.Fatalf("units = %v, want one per directory %v", unitNames(units), want)
	}
	billing := unitNamed(t, units, "internal/billing")
	want := []string{"internal/billing/charge.go", "internal/billing/refund.go"}
	if !reflect.DeepEqual(billing.Files, want) {
		t.Errorf("files = %v, want every source file in that directory %v", billing.Files, want)
	}
}

func TestTheFileGroupingProducesOneUnitPerSourceFile(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"internal/billing/charge.go": "package billing\n",
		"internal/billing/refund.go": "package billing\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Group: discover.Grouping("file"),
	})

	want := []string{"internal/billing/charge.go", "internal/billing/refund.go"}
	if !reflect.DeepEqual(unitNames(units), want) {
		t.Fatalf("units = %v, want one per file %v", unitNames(units), want)
	}
	for _, u := range units {
		if !reflect.DeepEqual(u.Files, []string{u.Name}) {
			t.Errorf("unit %q holds %v, want exactly the one file", u.Name, u.Files)
		}
	}
}

func TestAnUnknownGroupingIsRejected(t *testing.T) {
	root := sourceTree(t, map[string]string{"main.go": "package main\n"})

	err := scanErr(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Group: discover.Grouping("package"),
	})

	want := `unknown grouping "package"; use dir or file`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestAnUnknownGroupingStopsBeforeAnythingIsScanned(t *testing.T) {
	// The requested path cannot be inspected, so a scan that had started would
	// report that instead of the grouping.
	root := sourceTree(t, map[string]string{"main.go": "package main\n"})

	err := scanErr(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors",
		Group: discover.Grouping("package"), Paths: []string{"does-not-exist"},
	})

	want := `unknown grouping "package"; use dir or file`
	if err.Error() != want {
		t.Errorf("error = %q, want the grouping to be rejected before any scanning: %q", err.Error(), want)
	}
}

func TestTheAcceptedGroupingNamesAreDirAndFile(t *testing.T) {
	if got := discover.Groupings(); !reflect.DeepEqual(got, []string{"dir", "file"}) {
		t.Errorf("Groupings() = %v, want [dir file]", got)
	}
}

// --- Choosing what counts as source ---------------------------------------

func TestALanguageWithNoKnownSourceExtensionsIsRejected(t *testing.T) {
	root := sourceTree(t, map[string]string{"payroll.cbl": "IDENTIFICATION DIVISION.\n"})

	err := scanErr(t, discover.Options{Root: root, Language: "cobol", BehaviorDir: "behaviors"})

	want := "katana does not know which files are cobol source; discovery needs one of: " +
		strings.Join(config.Languages(), ", ")
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestAScanWithNoPathsWalksTheWholeProject(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"main.go":                    "package main\n",
		"internal/billing/charge.go": "package billing\n",
		"web/handler/api.go":         "package handler\n",
	})

	units := scanGo(t, root)

	want := []string{"internal/billing/charge.go", "main.go", "web/handler/api.go"}
	if got := scannedFiles(units); !reflect.DeepEqual(got, want) {
		t.Errorf("scanned %v, want the whole project %v", got, want)
	}
}

func TestAScanWithPathsWalksOnlyThosePaths(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"keep/one.go":       "package keep\n",
		"keep/deep/two.go":  "package deep\n",
		"other/three.go":    "package other\n",
		"loose/single.go":   "package loose\n",
		"untouched/four.go": "package untouched\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors",
		Paths: []string{"keep", "loose/single.go"},
	})

	want := []string{"keep/deep/two.go", "keep/one.go", "loose/single.go"}
	if got := scannedFiles(units); !reflect.DeepEqual(got, want) {
		t.Errorf("scanned %v, want only the requested paths %v", got, want)
	}
}

func TestAPathThatCannotBeInspectedIsReportedWithItsName(t *testing.T) {
	root := sourceTree(t, map[string]string{"main.go": "package main\n"})

	err := scanErr(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Paths: []string{"nope/missing.go"},
	})

	if prefix := `"nope/missing.go": `; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), prefix)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want the underlying reason to be reported", err)
	}
}

func TestANamedFileMustBeSourceForTheChosenLanguage(t *testing.T) {
	root := sourceTree(t, map[string]string{"README.md": "# readme\n"})

	err := scanErr(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Paths: []string{"README.md"},
	})

	want := `"README.md" is not a go source file`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestANamedFileIsAcceptedWhenItIsSourceForTheChosenLanguage(t *testing.T) {
	root := sourceTree(t, map[string]string{"internal/app/service.go": "package app\n"})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Paths: []string{"internal/app/service.go"},
	})

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"internal/app/service.go"}) {
		t.Errorf("scanned %v, want [internal/app/service.go]", got)
	}
}

func TestAFileNamedOnTheCommandLineIsIncludedEvenWhenItIsTestCode(t *testing.T) {
	root := sourceTree(t, map[string]string{"internal/app/service_test.go": "package app\n"})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors",
		Paths: []string{"internal/app/service_test.go"},
	})

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"internal/app/service_test.go"}) {
		t.Errorf("scanned %v, want the test file the user pointed at", got)
	}
}

func TestAFileNamedOnTheCommandLineIsIncludedEvenWhenItLooksGenerated(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"api/service.pb.go": "// Code generated by protoc. DO NOT EDIT.\npackage api\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Paths: []string{"api/service.pb.go"},
	})

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"api/service.pb.go"}) {
		t.Errorf("scanned %v, want the generated file the user pointed at", got)
	}
}

func TestAScanThatFindsNoSourceFilesReturnsNothingAndNoError(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"README.md":  "# readme\n",
		"docs/a.txt": "notes\n",
	})

	units, err := discover.Scan(discover.Options{Root: root, Language: "go", BehaviorDir: "behaviors"})
	if err != nil {
		t.Fatalf("Scan: %v, want no error", err)
	}
	if len(units) != 0 {
		t.Errorf("units = %+v, want none", units)
	}
}

// --- What a directory walk skips ------------------------------------------

func TestDirectoriesWhoseNameBeginsWithADotAreNotDescendedInto(t *testing.T) {
	// .git and .katana are covered by the same rule rather than by name.
	root := sourceTree(t, map[string]string{
		".git/hook.go":     "package hook\n",
		".katana/state.go": "package state\n",
		".hidden/thing.go": "package hidden\n",
		"keep/main.go":     "package keep\n",
	})

	units := scanGo(t, root)

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"keep/main.go"}) {
		t.Errorf("scanned %v, want only [keep/main.go]", got)
	}
}

func TestTheSkippedDirectoryNames(t *testing.T) {
	skipped := []string{
		"vendor", "node_modules", "bower_components", "third_party", "thirdparty",
		"dist", "build", "out", "target", "bin", "obj", "coverage",
		"__pycache__", "site-packages", "venv", "_build", "pods", "deriveddata", "generated",
	}

	for _, name := range skipped {
		t.Run(name, func(t *testing.T) {
			root := sourceTree(t, map[string]string{
				name + "/lib.go": "package lib\n",
				"keep/main.go":   "package keep\n",
			})

			units := scanGo(t, root)

			if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"keep/main.go"}) {
				t.Errorf("scanned %v, want %q not to be descended into", got, name)
			}
		})
	}
}

func TestSkippedDirectoryNamesAreMatchedWithoutRegardToLetterCase(t *testing.T) {
	for _, name := range []string{"Vendor", "NODE_MODULES", "Build", "DerivedData"} {
		t.Run(name, func(t *testing.T) {
			root := sourceTree(t, map[string]string{
				name + "/lib.go": "package lib\n",
				"keep/main.go":   "package keep\n",
			})

			units := scanGo(t, root)

			if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"keep/main.go"}) {
				t.Errorf("scanned %v, want %q not to be descended into", got, name)
			}
		})
	}
}

func TestTheKatanaConfigurationDirectoryIsNotDescendedInto(t *testing.T) {
	root := sourceTree(t, map[string]string{
		config.Dir + "/cache.go": "package cache\n",
		"keep/main.go":           "package keep\n",
	})

	units := scanGo(t, root)

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"keep/main.go"}) {
		t.Errorf("scanned %v, want katana's own state directory left out", got)
	}
}

func TestTheConfiguredBehaviorDirectoryIsNotDescendedInto(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"docs/specs/existing.go": "package specs\n",
		"keep/main.go":           "package keep\n",
	})

	units := scanUnits(t, discover.Options{Root: root, Language: "go", BehaviorDir: "docs/specs"})

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"keep/main.go"}) {
		t.Errorf("scanned %v, want existing specifications never treated as source", got)
	}
}

func TestADirectoryAskedForOnTheCommandLineIsAlwaysDescendedInto(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"vendor/lib.go": "package lib\n",
		"keep/main.go":  "package keep\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Paths: []string{"vendor"},
	})

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"vendor/lib.go"}) {
		t.Errorf("scanned %v, want the requested directory scanned whatever its name", got)
	}
}

func TestAnExcludedNameMatchesADirectoryAtAnyDepth(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"legacy/old.go":          "package legacy\n",
		"internal/legacy/old.go": "package legacy\n",
		"internal/keep.go":       "package internal\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Exclude: []string{"legacy"},
	})

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"internal/keep.go"}) {
		t.Errorf("scanned %v, want every directory named legacy excluded", got)
	}
}

func TestAnExcludedEntryMatchesAnExactProjectRelativePath(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"legacy/old.go":          "package legacy\n",
		"internal/legacy/old.go": "package legacy\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Exclude: []string{"internal/legacy"},
	})

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"legacy/old.go"}) {
		t.Errorf("scanned %v, want only the named path excluded", got)
	}
}

func TestAnExcludedPathAlsoExcludesDirectoriesBeneathIt(t *testing.T) {
	// The excluded directory is asked for by name, so the walk starts inside it;
	// what the entry still excludes is everything below.
	root := sourceTree(t, map[string]string{
		"internal/legacy/old.go":      "package legacy\n",
		"internal/legacy/deep/old.go": "package deep\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors",
		Paths: []string{"internal/legacy"}, Exclude: []string{"internal/legacy"},
	})

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"internal/legacy/old.go"}) {
		t.Errorf("scanned %v, want directories beneath the excluded path left out", got)
	}
}

func TestExclusionEntriesThatAreEmptyOrTheProjectRootAreIgnored(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"internal/keep.go": "package internal\n",
		"main.go":          "package main\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Exclude: []string{"", ".", "./"},
	})

	want := []string{"internal/keep.go", "main.go"}
	if got := scannedFiles(units); !reflect.DeepEqual(got, want) {
		t.Errorf("scanned %v, want nothing excluded %v", got, want)
	}
}

func TestTestFilesAreLeftOutOfADirectoryWalk(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"internal/app/service.go":      "package app\n",
		"internal/app/service_test.go": "package app\n",
	})

	units := scanGo(t, root)

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"internal/app/service.go"}) {
		t.Errorf("scanned %v, want test code left out by default", got)
	}
}

func TestTestFilesAreKeptWhenTestsAreExplicitlyIncluded(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"internal/app/service.go":      "package app\n",
		"internal/app/service_test.go": "package app\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", IncludeTests: true,
	})

	want := []string{"internal/app/service.go", "internal/app/service_test.go"}
	if got := scannedFiles(units); !reflect.DeepEqual(got, want) {
		t.Errorf("scanned %v, want %v", got, want)
	}
}

// --- What counts as generated ---------------------------------------------

func TestGeneratedFileNamesAreLeftOutOfADirectoryWalk(t *testing.T) {
	// Each suffix is checked in a language whose source it could be. `.pb.cc`
	// and `.g.dart` have no such language — katana knows no C++ or Dart
	// extensions — so a walk can never reach a file with those suffixes.
	cases := []struct{ language, generated, kept string }{
		{"javascript", "web/bundle.min.js", "web/app.js"},
		{"typescript", "web/bundle.min.ts", "web/app.ts"},
		{"go", "api/service.pb.go", "api/server.go"},
		{"go", "api/service.pb.gw.go", "api/server.go"},
		{"python", "api/service_pb2.py", "api/server.py"},
		{"python", "api/service_pb2_grpc.py", "api/server.py"},
		{"php", "api/Service.pb.php", "api/Server.php"},
		{"go", "api/model_generated.go", "api/server.go"},
		{"go", "api/model.gen.go", "api/server.go"},
		{"csharp", "Api/Model.generated.cs", "Api/Server.cs"},
		{"csharp", "Api/Model.designer.cs", "Api/Server.cs"},
		{"csharp", "Api/Model.g.cs", "Api/Server.cs"},
		{"typescript", "web/types.d.ts", "web/app.ts"},
	}

	for _, c := range cases {
		t.Run(c.generated, func(t *testing.T) {
			root := sourceTree(t, map[string]string{
				c.generated: "// code\n",
				c.kept:      "// code\n",
			})

			units := scanUnits(t, discover.Options{Root: root, Language: c.language, BehaviorDir: "behaviors"})

			if got := scannedFiles(units); !reflect.DeepEqual(got, []string{c.kept}) {
				t.Errorf("scanned %v, want %q left out as generated", got, c.generated)
			}
		})
	}
}

func TestGeneratedFileNamesAreMatchedWithoutRegardToLetterCase(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"api/SERVICE.PB.GO": "// code\n",
		"api/server.go":     "// code\n",
	})

	units := scanGo(t, root)

	if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"api/server.go"}) {
		t.Errorf("scanned %v, want the upper-cased generated name left out", got)
	}
}

func TestAGeneratedHeaderInTheFirstKilobyteLeavesTheFileOut(t *testing.T) {
	for _, header := range []string{
		"// Code generated by protoc. DO NOT EDIT.",
		"// do not edit",
		"// @generated",
		"// @GENERATED by a tool",
	} {
		t.Run(header, func(t *testing.T) {
			root := sourceTree(t, map[string]string{
				"api/model.go":  header + "\n\npackage api\n",
				"api/server.go": "package api\n",
			})

			units := scanGo(t, root)

			if got := scannedFiles(units); !reflect.DeepEqual(got, []string{"api/server.go"}) {
				t.Errorf("scanned %v, want the file headed %q left out", got, header)
			}
		})
	}
}

func TestAGeneratedMarkerAfterTheFirstKilobyteDoesNotCount(t *testing.T) {
	// Only the first 1024 bytes are inspected; this marker starts at byte 1500.
	body := strings.Repeat("// filler line\n", 100) + "// DO NOT EDIT\npackage api\n"
	root := sourceTree(t, map[string]string{
		"api/model.go":  body,
		"api/server.go": "package api\n",
	})

	units := scanGo(t, root)

	want := []string{"api/model.go", "api/server.go"}
	if got := scannedFiles(units); !reflect.DeepEqual(got, want) {
		t.Errorf("scanned %v, want %v", got, want)
	}
}

func TestAFileThatCannotBeOpenedIsNotTreatedAsGenerated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file with no permission bits set")
	}
	root := sourceTree(t, map[string]string{
		"api/locked.go": "package api\n",
		"api/server.go": "package api\n",
	})
	locked := filepath.Join(root, "api", "locked.go")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	units := scanGo(t, root)

	want := []string{"api/locked.go", "api/server.go"}
	if got := scannedFiles(units); !reflect.DeepEqual(got, want) {
		t.Errorf("scanned %v, want an unreadable header to leave the file in %v", got, want)
	}
}

// --- The units a scan produces --------------------------------------------

func TestADirectoryUnitIsNamedAfterItsProjectRelativeDirectory(t *testing.T) {
	root := sourceTree(t, map[string]string{"internal/billing/charge.go": "package billing\n"})

	units := scanGo(t, root)

	if got := unitNames(units); !reflect.DeepEqual(got, []string{"internal/billing"}) {
		t.Errorf("names = %v, want [internal/billing]", got)
	}
}

func TestAFileUnitIsNamedAfterItsProjectRelativeFile(t *testing.T) {
	root := sourceTree(t, map[string]string{"internal/billing/charge.go": "package billing\n"})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Group: discover.Grouping("file"),
	})

	if got := unitNames(units); !reflect.DeepEqual(got, []string{"internal/billing/charge.go"}) {
		t.Errorf("names = %v, want [internal/billing/charge.go]", got)
	}
}

func TestADirectoryUnitListsItsSourceFilesInSortedOrder(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"internal/billing/refund.go": "package billing\n",
		"internal/billing/apply.go":  "package billing\n",
		"internal/billing/charge.go": "package billing\n",
	})

	u := unitNamed(t, scanGo(t, root), "internal/billing")

	want := []string{"internal/billing/apply.go", "internal/billing/charge.go", "internal/billing/refund.go"}
	if !reflect.DeepEqual(u.Files, want) {
		t.Errorf("files = %v, want %v", u.Files, want)
	}
}

func TestADirectoryUnitReportsTheTotalSizeOfItsFiles(t *testing.T) {
	charge, refund := "package billing\n", "package billing // and more\n"
	root := sourceTree(t, map[string]string{
		"internal/billing/charge.go": charge,
		"internal/billing/refund.go": refund,
	})

	u := unitNamed(t, scanGo(t, root), "internal/billing")

	if want := int64(len(charge) + len(refund)); u.Bytes != want {
		t.Errorf("Bytes = %d, want the total %d", u.Bytes, want)
	}
}

func TestAFileUnitReportsTheSizeOfItsSingleFile(t *testing.T) {
	charge := "package billing\n"
	root := sourceTree(t, map[string]string{
		"internal/billing/charge.go": charge,
		"internal/billing/refund.go": "package billing // longer\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Group: discover.Grouping("file"),
	})
	u := unitNamed(t, units, "internal/billing/charge.go")

	if want := int64(len(charge)); u.Bytes != want {
		t.Errorf("Bytes = %d, want %d", u.Bytes, want)
	}
}

func TestAUnitForFilesAtTheProjectRootRecordsItsDirectoryAsADot(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"main.go":                    "package main\n",
		"internal/billing/charge.go": "package billing\n",
	})

	u := unitNamed(t, scanGo(t, root), ".")

	if u.Dir != "." {
		t.Errorf("Dir = %q, want \".\"", u.Dir)
	}
}

func TestUnitsComeBackSortedByTheBehaviorFileTheyWrite(t *testing.T) {
	root := namedSourceTree(t, "zeta", map[string]string{
		"web/handler/api.go":         "package handler\n",
		"internal/billing/charge.go": "package billing\n",
		"main.go":                    "package main\n",
		"api/server.go":              "package api\n",
	})

	outputs := unitOutputs(scanGo(t, root))

	if !sort.StringsAreSorted(outputs) {
		t.Errorf("outputs = %v, want them sorted", outputs)
	}
	want := []string{
		"behaviors/api.md",
		"behaviors/internal/billing.md",
		"behaviors/web/handler.md",
		"behaviors/zeta.md",
	}
	if !reflect.DeepEqual(outputs, want) {
		t.Errorf("outputs = %v, want %v", outputs, want)
	}
}

// --- Where each unit's specification is written ---------------------------

func TestABehaviorFileMirrorsTheSourceTree(t *testing.T) {
	root := sourceTree(t, map[string]string{"internal/config/config.go": "package config\n"})

	u := unitNamed(t, scanGo(t, root), "internal/config")

	if u.Output != "behaviors/internal/config.md" {
		t.Errorf("Output = %q, want behaviors/internal/config.md", u.Output)
	}
}

func TestAFileGroupedUnitKeepsItsDirectoryAndBaseNameWithAnMdExtension(t *testing.T) {
	root := sourceTree(t, map[string]string{"internal/config/config.go": "package config\n"})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "go", BehaviorDir: "behaviors", Group: discover.Grouping("file"),
	})
	u := unitNamed(t, units, "internal/config/config.go")

	if u.Output != "behaviors/internal/config/config.md" {
		t.Errorf("Output = %q, want behaviors/internal/config/config.md", u.Output)
	}
}

func TestARootUnitIsNamedAfterTheProjectRootDirectory(t *testing.T) {
	root := namedSourceTree(t, "myproj", map[string]string{"main.go": "package main\n"})

	u := unitNamed(t, scanGo(t, root), ".")

	if u.Output != "behaviors/myproj.md" {
		t.Errorf("Output = %q, want behaviors/myproj.md rather than a file named after \".\"", u.Output)
	}
}

func TestARootUnitIsNamedRootWhenTheProjectRootHasNoUsableName(t *testing.T) {
	// "." is the project root with no directory name of its own to borrow.
	tree := sourceTree(t, map[string]string{"main.go": "package main\n"})
	t.Chdir(tree)

	units := scanUnits(t, discover.Options{Root: ".", Language: "go", BehaviorDir: "behaviors"})
	u := unitNamed(t, units, ".")

	if u.Output != "behaviors/root.md" {
		t.Errorf("Output = %q, want behaviors/root.md", u.Output)
	}
}

func TestAnEmptyBehaviorDirectoryIsTreatedAsBehaviors(t *testing.T) {
	root := sourceTree(t, map[string]string{"internal/config/config.go": "package config\n"})

	u := unitNamed(t, scanUnits(t, discover.Options{Root: root, Language: "go", BehaviorDir: ""}), "internal/config")

	if u.Output != "behaviors/internal/config.md" {
		t.Errorf("Output = %q, want behaviors/internal/config.md", u.Output)
	}
}

func TestABehaviorDirectoryThatReducesToASlashIsTreatedAsBehaviors(t *testing.T) {
	root := sourceTree(t, map[string]string{"internal/config/config.go": "package config\n"})

	u := unitNamed(t, scanUnits(t, discover.Options{Root: root, Language: "go", BehaviorDir: "/"}), "internal/config")

	if u.Output != "behaviors/internal/config.md" {
		t.Errorf("Output = %q, want behaviors/internal/config.md", u.Output)
	}
}

func TestTwoFileUnitsThatWouldCollideAreDistinguishedByTheirExtension(t *testing.T) {
	root := sourceTree(t, map[string]string{
		"web/handler.js":  "export const a = 1\n",
		"web/handler.jsx": "export const b = 2\n",
	})

	units := scanUnits(t, discover.Options{
		Root: root, Language: "javascript", BehaviorDir: "behaviors", Group: discover.Grouping("file"),
	})

	want := []string{"behaviors/web/handler_js.md", "behaviors/web/handler_jsx.md"}
	if got := unitOutputs(units); !reflect.DeepEqual(got, want) {
		t.Errorf("outputs = %v, want %v so neither overwrites the other", got, want)
	}
}

func TestACollidingRootUnitIsDistinguishedBySuffixRoot(t *testing.T) {
	// The project is named "app" and holds a package named "app": both want
	// behaviors/app.md.
	root := namedSourceTree(t, "app", map[string]string{
		"main.go":   "package main\n",
		"app/x.go":  "package app\n",
		"keep/y.go": "package keep\n",
	})

	u := unitNamed(t, scanGo(t, root), ".")

	if u.Output != "behaviors/app_root.md" {
		t.Errorf("Output = %q, want behaviors/app_root.md", u.Output)
	}
}

func TestACollidingDirectoryUnitKeepsItsOutputPath(t *testing.T) {
	// Only a root unit can be distinguished under the dir grouping, so the
	// directory unit keeps the name it derived from its own path.
	root := namedSourceTree(t, "app", map[string]string{
		"main.go":  "package main\n",
		"app/x.go": "package app\n",
	})

	u := unitNamed(t, scanGo(t, root), "app")

	if u.Output != "behaviors/app.md" {
		t.Errorf("Output = %q, want behaviors/app.md", u.Output)
	}
}

// --- The fake harness ------------------------------------------------------

const (
	helperEnv       = "KATANA_DISCOVER_HELPER"
	helperWritePath = "KATANA_DISCOVER_HELPER_WRITE_PATH"
	helperWriteBody = "KATANA_DISCOVER_HELPER_WRITE_BODY"
	helperStdout    = "KATANA_DISCOVER_HELPER_STDOUT"
	helperStderr    = "KATANA_DISCOVER_HELPER_STDERR"
	helperExit      = "KATANA_DISCOVER_HELPER_EXIT"
)

// TestDiscoverHelperProcess is not a test of its own: it is the coding agent
// every discovery test runs. A harness is an executable, so the only way to
// drive one from outside the package is to be one — this test binary re-runs
// itself with the environment below describing what the agent should do.
func TestDiscoverHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("the fake harness, run only as a child process of a discovery test")
	}
	// The prompt arrives on stdin; a real agent reads it.
	_, _ = io.Copy(io.Discard, os.Stdin)

	if p := os.Getenv(helperWritePath); p != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(90)
		}
		if err := os.WriteFile(p, []byte(os.Getenv(helperWriteBody)), 0o644); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(91)
		}
	}
	os.Stdout.WriteString(os.Getenv(helperStdout))
	os.Stderr.WriteString(os.Getenv(helperStderr))

	code, _ := strconv.Atoi(os.Getenv(helperExit))
	os.Exit(code) // exit before the testing package prints its own report
}

// fakeAgent describes what the harness does for one discovery.
type fakeAgent struct {
	name      string // harness name; "fake" when empty
	writePath string // project-relative behavior file it writes, if any
	writeBody string
	stdout    string
	stderr    string
	exit      int
}

func newRunner(t *testing.T, root string, a fakeAgent) *harness.Runner {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot find the test binary to use as a harness: %v", err)
	}
	name := a.name
	if name == "" {
		name = "fake"
	}
	runner, err := harness.New(name, harness.Spec{
		Command: exe,
		Args:    []string{"-test.run=^TestDiscoverHelperProcess$"},
		Prompt:  harness.PromptStdin,
	}, harness.Options{
		Dir:    root,
		Stderr: io.Discard,
		Env: map[string]string{
			helperEnv:       "1",
			helperWritePath: a.writePath,
			helperWriteBody: a.writeBody,
			helperStdout:    a.stdout,
			helperStderr:    a.stderr,
			helperExit:      strconv.Itoa(a.exit),
		},
	})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	return runner
}

// --- Discovery helpers -----------------------------------------------------

const (
	specOutput = "behaviors/internal/billing.md"
	specSource = "internal/billing/charge.go"
)

// sampleUnit is the unit every discovery test runs: one package, two files, one
// behavior file to write.
func sampleUnit() discover.Unit {
	return discover.Unit{
		Name:   "internal/billing",
		Dir:    "internal/billing",
		Files:  []string{specSource, "internal/billing/refund.go"},
		Bytes:  120,
		Output: specOutput,
	}
}

// sampleRequest is that unit as a request. Discover reads ExistingBehavior off
// disk itself, so a request never carries it.
func sampleRequest() discover.Request {
	return discover.Request{Unit: sampleUnit(), Language: "go"}
}

func specPath(root string) string { return filepath.Join(root, filepath.FromSlash(specOutput)) }

func newDiscoverer(t *testing.T, root string, a fakeAgent) *discover.Discoverer {
	t.Helper()
	return discover.New(newRunner(t, root, a), root)
}

func discoverWith(t *testing.T, root string, a fakeAgent, req discover.Request) (*discover.Outcome, error) {
	t.Helper()
	return newDiscoverer(t, root, a).Discover(context.Background(), req)
}

func mustDiscover(t *testing.T, root string, a fakeAgent) *discover.Outcome {
	t.Helper()
	out, err := discoverWith(t, root, a, sampleRequest())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return out
}

func discoverErr(t *testing.T, root string, a fakeAgent) error {
	t.Helper()
	out, err := discoverWith(t, root, a, sampleRequest())
	if err == nil {
		t.Fatalf("Discover succeeded, want an error; outcome = %+v", out)
	}
	return err
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// specBody is a reply that reads as a specification: a heading and two bullets.
const specBody = "# Billing\n\n## Charging a card\n\n- A charge below the minimum is rejected.\n- A declined card leaves the order unpaid.\n"

// --- Asking the harness for one unit's specification ----------------------

func TestAnAbsentBehaviorFileCountsAsEmptyRatherThanAnError(t *testing.T) {
	root := t.TempDir()
	d := newDiscoverer(t, root, fakeAgent{writePath: specOutput, writeBody: specBody})
	var prompt string
	d.OnPrompt = func(p string) { prompt = p }

	if _, err := d.Discover(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("Discover: %v, want a missing behavior file to be treated as empty", err)
	}
	if strings.Contains(prompt, "Existing specification") {
		t.Errorf("prompt offered an existing specification when there is no file:\n%s", prompt)
	}
}

func TestTheExistingBehaviorFileIsReadBeforeTheHarnessRuns(t *testing.T) {
	existing := "# Billing\n\n- A charge is taken once.\n- A refund is never larger than the charge.\n"
	root := sourceTree(t, map[string]string{specOutput: existing})
	d := newDiscoverer(t, root, fakeAgent{writePath: specOutput, writeBody: specBody})
	var prompt string
	d.OnPrompt = func(p string) { prompt = p }

	if _, err := d.Discover(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !strings.Contains(prompt, existing) {
		t.Errorf("prompt does not carry the specification already on disk:\n%s", prompt)
	}
}

func TestThePromptIsGivenToTheObserverBeforeTheHarnessIsStarted(t *testing.T) {
	root := t.TempDir()
	d := newDiscoverer(t, root, fakeAgent{writePath: specOutput, writeBody: specBody})
	called := false
	harnessHadRun := false
	d.OnPrompt = func(p string) {
		called = true
		// The harness writes the behavior file, so its presence dates the call.
		_, err := os.Stat(specPath(root))
		harnessHadRun = err == nil
		if !strings.Contains(p, specSource) {
			t.Errorf("the observer was given something other than the finished prompt:\n%s", p)
		}
	}

	if _, err := d.Discover(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !called {
		t.Fatal("the prompt observer was never called")
	}
	if harnessHadRun {
		t.Error("the prompt was shown only after the harness had already written the file")
	}
}

func TestNoPromptObserverIsNeeded(t *testing.T) {
	// The observer is optional: an unset one is not called, not a failure.
	root := t.TempDir()

	out := mustDiscover(t, root, fakeAgent{writePath: specOutput, writeBody: specBody})

	if !out.WroteFile {
		t.Errorf("outcome = %+v, want the discovery to run without an observer", out)
	}
}

func TestAHarnessThatFailsToRunFailsTheDiscovery(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{exit: 3, stderr: "the agent could not start"})

	if !strings.Contains(err.Error(), "exited with status 3") {
		t.Errorf("error = %q, want the harness's own error", err.Error())
	}
	if !strings.Contains(err.Error(), "the agent could not start") {
		t.Errorf("error = %q, want it to carry what the harness reported", err.Error())
	}
}

// --- What the prompt asks for ---------------------------------------------

func assertPromptContains(t *testing.T, prompt string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(prompt, w) {
			t.Errorf("prompt does not say %q:\n%s", w, prompt)
		}
	}
}

func TestThePromptNamesTheLanguageAndEverySourceFile(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "go source files")
	for _, f := range sampleUnit().Files {
		assertPromptContains(t, prompt, f)
	}
}

func TestThePromptStatesTheListedFilesAreTheOnlySourceOfWhatIsWritten(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "the only source of what you write")
}

func TestThePromptNamesTheExactProjectRelativeOutputPath(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, specOutput, "relative to the current working directory")
	if strings.Contains(prompt, filepath.Join(string(filepath.Separator), "tmp")) {
		t.Errorf("prompt should name a project-relative path, not an absolute one:\n%s", prompt)
	}
}

func TestThePromptTellsTheHarnessToCreateParentDirectories(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "Create any parent directories the path needs")
}

func TestThePromptTellsTheHarnessToWriteTheFileItselfRatherThanPrintIt(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt,
		"Write the file yourself using your file tools",
		"do not print the specification as your reply")
}

func TestThePromptAllowsPrintingTheFileOnlyWhenWritingIsImpossible(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt,
		"If, and only if, writing that file fails",
		"no file tool is available",
		"denied by a permission check",
		"single fenced code block",
		"as your entire reply")
}

func TestThePromptTellsTheHarnessNotToStopOrAskForAccess(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "do not stop and do not ask for access")
}

func TestThePromptShowsTheRequiredMarkdownShape(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt,
		"# What this part of the product does",           // a title
		"One or two sentences of context",                // context
		"## A group of related behavior",                 // one section per group
		"- A single statement of observable behavior")    // bullets stating one fact
}

func TestThePromptRequiresObservableBehaviorOnly(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "Describe observable behavior", "Never how it is implemented")
}

func TestThePromptRequiresOneCheckableFactPerBullet(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "One bullet per fact that could be checked on its own")
}

func TestThePromptRequiresRealErrorMessagesAndLiteralValuesQuoted(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "quote real error messages and literal values")
}

func TestThePromptRequiresEdgeCasesToBeCovered(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "Cover the edge cases the code handles")
}

func TestThePromptForbidsCodeFencesSignaturesAndFilePathsInTheResult(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "Do not include code, code fences, function signatures or file paths")
}

func TestThePromptForbidsModifyingAnyOtherFileAndWritingTests(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt, "Do not modify any file other than the one named above", "Do not write tests")
}

func TestAnExistingSpecificationIsIncludedInThePrompt(t *testing.T) {
	existing := "# Billing\n\n- A charge is taken once.\n"
	req := sampleRequest()
	req.ExistingBehavior = existing

	prompt := discover.BuildPrompt(req)

	assertPromptContains(t, prompt, existing,
		"correct statements the code contradicts",
		"add behavior it has gained",
		"remove behavior it no longer has",
		"Keep the wording, ordering and structure of everything still true",
		"edited by hand")
}

func TestAnEmptyExistingSpecificationAddsNoSectionToThePrompt(t *testing.T) {
	for _, existing := range []string{"", "   \n\t\n"} {
		req := sampleRequest()
		req.ExistingBehavior = existing

		prompt := discover.BuildPrompt(req)

		if strings.Contains(prompt, "Existing specification") {
			t.Errorf("existing behavior %q added a section to the prompt:\n%s", existing, prompt)
		}
	}
}

func TestExtraInstructionsAppearAsTheirOwnSectionAfterTheRequirements(t *testing.T) {
	req := sampleRequest()
	req.ExtraInstructions = "Stub the payment gateway."

	prompt := discover.BuildPrompt(req)

	assertPromptContains(t, prompt, "## Additional project instructions", "Stub the payment gateway.")
	if requirements, extra := strings.Index(prompt, "## Requirements"), strings.Index(prompt, "## Additional project instructions"); extra < requirements {
		t.Errorf("extra instructions appear at %d, before the requirements at %d", extra, requirements)
	}
}

func TestWhitespaceOnlyExtraInstructionsAddNoSection(t *testing.T) {
	req := sampleRequest()
	req.ExtraInstructions = "   \n  "

	prompt := discover.BuildPrompt(req)

	if strings.Contains(prompt, "Additional project instructions") {
		t.Errorf("whitespace instructions added a section to the prompt:\n%s", prompt)
	}
}

func TestThePromptAsksForASkipLineWhenThereIsNoBehaviorToSpecify(t *testing.T) {
	prompt := discover.BuildPrompt(sampleRequest())

	assertPromptContains(t, prompt,
		"generated code, plain data declarations, constants",
		"only wires other packages together",
		"do not write the file",
		"Reply with one line, and nothing else",
		"SKIP: <the reason, in a few words>")
}

// --- How the harness's reply is interpreted -------------------------------

func TestASkipWithNothingWrittenIsReportedAsSkipped(t *testing.T) {
	root := t.TempDir()

	out := mustDiscover(t, root, fakeAgent{stdout: "SKIP: only constants here\n"})

	if !out.Skipped {
		t.Fatalf("outcome = %+v, want a skip", out)
	}
	if out.Reason != "only constants here" {
		t.Errorf("Reason = %q, want %q", out.Reason, "only constants here")
	}
	if _, err := os.Stat(specPath(root)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a skipped unit should leave no behavior file, stat gave %v", err)
	}
}

func TestASkipIsRecognisedOnALineOfItsOwnAfterLeadingMarkupAndWhitespace(t *testing.T) {
	for _, line := range []string{
		"SKIP: only constants here",
		"   SKIP: only constants here",
		"**SKIP: only constants here**",
		"# SKIP: only constants here",
		"- SKIP: only constants here",
		"> SKIP: only constants here",
		"`SKIP: only constants here`",
		"I read every file.\n\nSKIP: only constants here\n",
	} {
		t.Run(line, func(t *testing.T) {
			root := t.TempDir()

			out := mustDiscover(t, root, fakeAgent{stdout: line})

			if !out.Skipped {
				t.Fatalf("outcome = %+v, want a skip", out)
			}
			if out.Reason != "only constants here" {
				t.Errorf("Reason = %q, want %q", out.Reason, "only constants here")
			}
		})
	}
}

func TestASkipMentionedInsideAParagraphIsCommentaryNotASkip(t *testing.T) {
	root := t.TempDir()
	stdout := "I had to SKIP: two files while writing the specification.\n\n```markdown\n" + specBody + "```\n"

	out := mustDiscover(t, root, fakeAgent{stdout: stdout})

	if out.Skipped {
		t.Errorf("outcome = %+v, want the mention treated as commentary", out)
	}
	if !out.FromStdout {
		t.Errorf("outcome = %+v, want the printed specification to be used", out)
	}
}

func TestTheSkipMarkerIsMatchedWithoutRegardToLetterCase(t *testing.T) {
	root := t.TempDir()

	out := mustDiscover(t, root, fakeAgent{stdout: "skip: nothing to say\n"})

	if !out.Skipped || out.Reason != "nothing to say" {
		t.Errorf("outcome = %+v, want a skip with reason %q", out, "nothing to say")
	}
}

func TestTrailingEmphasisIsStrippedFromASkipReason(t *testing.T) {
	root := t.TempDir()

	out := mustDiscover(t, root, fakeAgent{stdout: "SKIP: no rules of its own*`_\n"})

	if out.Reason != "no rules of its own" {
		t.Errorf("Reason = %q, want the emphasis stripped", out.Reason)
	}
}

func TestASkipWithNoReasonIsReportedWithADefaultReason(t *testing.T) {
	root := t.TempDir()

	out := mustDiscover(t, root, fakeAgent{stdout: "SKIP:\n"})

	if !out.Skipped {
		t.Fatalf("outcome = %+v, want a skip", out)
	}
	if out.Reason != "no behavior to specify" {
		t.Errorf("Reason = %q, want %q", out.Reason, "no behavior to specify")
	}
}

func TestALongSkipReasonIsCutToTwoHundredCharacters(t *testing.T) {
	root := t.TempDir()
	reason := strings.Repeat("x", 250)

	out := mustDiscover(t, root, fakeAgent{stdout: "SKIP: " + reason + "\n"})

	want := strings.Repeat("x", 200) + "…"
	if out.Reason != want {
		t.Errorf("Reason = %q (%d characters), want it cut to 200 with an ellipsis", out.Reason, len(out.Reason))
	}
}

func TestASkipIsIgnoredWhenTheBehaviorFileChangedDuringTheRun(t *testing.T) {
	root := t.TempDir()

	out := mustDiscover(t, root, fakeAgent{
		writePath: specOutput,
		writeBody: specBody,
		stdout:    "SKIP: nothing here after all\n",
	})

	if out.Skipped {
		t.Errorf("outcome = %+v, want the written file to win over the skip", out)
	}
	if !out.WroteFile {
		t.Errorf("outcome = %+v, want the file the harness wrote to be reported", out)
	}
	if got := readFileString(t, specPath(root)); got != specBody {
		t.Errorf("behavior file = %q, want the harness's file kept", got)
	}
}

func TestANonEmptyBehaviorFileIsReportedAsWrittenByTheHarness(t *testing.T) {
	root := t.TempDir()

	out := mustDiscover(t, root, fakeAgent{writePath: specOutput, writeBody: specBody, stdout: "Wrote it.\n"})

	if !out.WroteFile {
		t.Fatalf("outcome = %+v, want WroteFile", out)
	}
	if out.FromStdout {
		t.Errorf("outcome = %+v, want the file, not the reply, to be the source", out)
	}
	if out.Bytes != len(specBody) {
		t.Errorf("Bytes = %d, want the file's size %d", out.Bytes, len(specBody))
	}
}

func TestAByteIdenticalBehaviorFileIsAlsoReportedAsUnchanged(t *testing.T) {
	root := sourceTree(t, map[string]string{specOutput: specBody})

	out := mustDiscover(t, root, fakeAgent{writePath: specOutput, writeBody: specBody})

	if !out.WroteFile {
		t.Fatalf("outcome = %+v, want WroteFile", out)
	}
	if !out.Unchanged {
		t.Errorf("outcome = %+v, want an identical specification reported as unchanged", out)
	}
}

func TestAChangedBehaviorFileIsNotReportedAsUnchanged(t *testing.T) {
	root := sourceTree(t, map[string]string{specOutput: "# Billing\n\n- An old fact.\n- Another old fact.\n"})

	out := mustDiscover(t, root, fakeAgent{writePath: specOutput, writeBody: specBody})

	if out.Unchanged {
		t.Errorf("outcome = %+v, want a rewritten specification not reported as unchanged", out)
	}
}

func TestEveryOutcomeCarriesTheHarnessReplyTrimmed(t *testing.T) {
	cases := []struct {
		name  string
		agent fakeAgent
	}{
		{"skipped", fakeAgent{stdout: "  \n SKIP: only constants here \n\n  "}},
		{"wrote the file", fakeAgent{writePath: specOutput, writeBody: specBody, stdout: "\n  Wrote it.  \n\n"}},
		{"recovered from the reply", fakeAgent{stdout: "\n\n```markdown\n" + specBody + "```\n\n  "}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()

			out := mustDiscover(t, root, c.agent)

			if want := strings.TrimSpace(c.agent.stdout); out.HarnessOutput != want {
				t.Errorf("HarnessOutput = %q, want %q", out.HarnessOutput, want)
			}
		})
	}
}

// --- Recovering a specification from the harness's reply ------------------

func TestASpecificationIsRecoveredFromTheReplyWhenNoFileWasWritten(t *testing.T) {
	root := t.TempDir()

	out := mustDiscover(t, root, fakeAgent{stdout: "```markdown\n" + specBody + "```\n"})

	if !out.FromStdout {
		t.Fatalf("outcome = %+v, want the specification taken from the reply", out)
	}
	if out.WroteFile {
		t.Errorf("outcome = %+v, want it not reported as a file the harness wrote", out)
	}
	if got := readFileString(t, specPath(root)); got != specBody {
		t.Errorf("behavior file = %q, want the recovered specification %q", got, specBody)
	}
}

func TestRecoveryPrefersTheLargestFencedBlock(t *testing.T) {
	root := t.TempDir()
	small := "# Note\n\n- One thing.\n- Another thing.\n"
	stdout := "First, a note:\n\n```\n" + small + "```\n\nAnd the file:\n\n```markdown\n" + specBody + "```\n"

	mustDiscover(t, root, fakeAgent{stdout: stdout})

	if got := readFileString(t, specPath(root)); got != specBody {
		t.Errorf("behavior file = %q, want the largest fenced block %q", got, specBody)
	}
}

func TestUnfencedOutputIsUsedWhenTheWholeReplyReadsAsASpecification(t *testing.T) {
	root := t.TempDir()

	out := mustDiscover(t, root, fakeAgent{stdout: specBody})

	if !out.FromStdout {
		t.Fatalf("outcome = %+v, want the unfenced specification used", out)
	}
	if got := readFileString(t, specPath(root)); got != specBody {
		t.Errorf("behavior file = %q, want %q", got, specBody)
	}
}

func TestTextWithoutAHeadingIsNotASpecification(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{stdout: "- A charge is taken once.\n- A refund is never larger.\n"})

	if !strings.Contains(err.Error(), "printed no specification") {
		t.Errorf("error = %q, want bullets with no heading rejected", err.Error())
	}
}

func TestTextWithFewerThanTwoBulletsIsNotASpecification(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{stdout: "# Summary\n\n- wrote one file\n"})

	if !strings.Contains(err.Error(), "printed no specification") {
		t.Errorf("error = %q, want a single bullet rejected", err.Error())
	}
}

func TestAgentChatterIsNeverWrittenToTheBehaviorFile(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{stdout: "Done — I read the files and wrote the behavior file.\n"})

	if !strings.Contains(err.Error(), "printed no specification") {
		t.Errorf("error = %q, want chatter rejected", err.Error())
	}
	if _, statErr := os.Stat(specPath(root)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("chatter should not be written to the behavior file, stat gave %v", statErr)
	}
}

func TestAStarBulletCountsTowardsASpecification(t *testing.T) {
	// The specification names "- " and "* " as bullet markers.
	root := t.TempDir()
	body := "# Billing\n\n* A charge below the minimum is rejected.\n* A declined card leaves the order unpaid.\n"

	out := mustDiscover(t, root, fakeAgent{stdout: body})

	if !out.FromStdout {
		t.Errorf("outcome = %+v, want star bullets accepted as a specification", out)
	}
}

func TestARecoveredSpecificationIsTrimmedWithASingleTrailingNewline(t *testing.T) {
	root := t.TempDir()
	stdout := "\n\n  \n" + specBody + "\n\n\n"

	mustDiscover(t, root, fakeAgent{stdout: stdout})

	if got := readFileString(t, specPath(root)); got != specBody {
		t.Errorf("behavior file = %q, want %q", got, specBody)
	}
}

func TestRecoveryCreatesMissingParentDirectories(t *testing.T) {
	root := t.TempDir()
	req := sampleRequest()
	req.Unit.Output = "behaviors/deep/nested/billing.md"

	out, err := discoverWith(t, root, fakeAgent{stdout: "```markdown\n" + specBody + "```\n"}, req)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if !out.FromStdout {
		t.Fatalf("outcome = %+v, want the specification recovered", out)
	}
	got := readFileString(t, filepath.Join(root, filepath.FromSlash(req.Unit.Output)))
	if got != specBody {
		t.Errorf("behavior file = %q, want %q", got, specBody)
	}
}

// --- When nothing usable comes back ---------------------------------------

func TestNothingUsableFromTheHarnessFailsTheUnit(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{stdout: "I could not do it."})

	want := `harness "fake" did not write ` + specOutput + `; harness said: I could not do it.`
	want = strings.Replace(want, "; harness said:", " and printed no specification; harness said:", 1)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestTheFailureSummaryQuotesStandardOutput(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{stdout: "nothing came of it", stderr: "some noise"})

	if !strings.HasSuffix(err.Error(), "harness said: nothing came of it") {
		t.Errorf("error = %q, want it to quote standard output", err.Error())
	}
}

func TestTheFailureSummaryFallsBackToErrorOutput(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{stdout: "", stderr: "the model returned nothing"})

	if !strings.HasSuffix(err.Error(), "harness said: the model returned nothing") {
		t.Errorf("error = %q, want it to quote error output when standard output is empty", err.Error())
	}
}

func TestTheFailureSummaryIsNoOutputWhenBothStreamsAreEmpty(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{})

	if !strings.HasSuffix(err.Error(), "harness said: (no output)") {
		t.Errorf("error = %q, want it to say (no output)", err.Error())
	}
}

func TestTheFailureSummaryIsFlattenedToASingleLine(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{stdout: "first line\nsecond line\nthird line"})

	if !strings.HasSuffix(err.Error(), "harness said: first line second line third line") {
		t.Errorf("error = %q, want the reply flattened onto one line", err.Error())
	}
}

func TestALongFailureSummaryIsCutToFourHundredCharacters(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{stdout: strings.Repeat("x", 500)})

	want := "harness said: " + strings.Repeat("x", 400) + "..."
	if !strings.HasSuffix(err.Error(), want) {
		t.Errorf("error = %q, want the summary cut to 400 characters with an ellipsis", err.Error())
	}
}

func TestAPermissionHintIsAppendedOnItsOwnIndentedLine(t *testing.T) {
	root := t.TempDir()
	stderr := "Error: permission denied writing " + specOutput

	err := discoverErr(t, root, fakeAgent{stderr: stderr})

	hint := harness.PermissionHint("fake", "", stderr)
	if hint == "" {
		t.Fatal("no permission hint is available for this output, so the test cannot observe the rule")
	}
	if !strings.HasSuffix(err.Error(), "\n  "+hint) {
		t.Errorf("error = %q, want it to end with the hint on its own indented line %q", err.Error(), hint)
	}
}

func TestNoPermissionHintIsAppendedWhenNothingLooksDenied(t *testing.T) {
	root := t.TempDir()

	err := discoverErr(t, root, fakeAgent{stdout: "I could not do it."})

	if strings.Contains(err.Error(), "hint:") {
		t.Errorf("error = %q, want no hint when the output shows no denial", err.Error())
	}
}

// hasString keeps the helper honest about being used; unit lists are compared
// directly everywhere else.
var _ = hasString
