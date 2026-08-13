package internal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/coverage"
)

func TestCoverageDetectsReportsByContentsRegardlessOfFilename(t *testing.T) {
	for _, tc := range []struct{ body, format string }{
		{"mode: atomic\n", coverage.FormatGo},
		{"<?xml version=\"1.0\"?><coverage/>", coverage.FormatCobertura},
		{"TN:\n", coverage.FormatLCOV},
		{"prefix\nSF:a.go\nend_of_record", coverage.FormatLCOV},
	} {
		if got := coverage.Detect([]byte(tc.body)); got != tc.format {
			t.Errorf("Detect(%q) = %q, want %q", tc.body, got, tc.format)
		}
	}
}

func TestCoverageUnknownReportExplainsAllSupportedFormats(t *testing.T) {
	_, err := coverage.Parse([]byte("not coverage"))
	if err == nil || !strings.Contains(err.Error(), "not a Go cover profile, LCOV or Cobertura XML") {
		t.Fatalf("Parse error = %v", err)
	}
}

func TestCoverageMalformedXMLNamesTheCoberturaParser(t *testing.T) {
	_, err := coverage.Parse([]byte("<coverage><"))
	if err == nil || !strings.HasPrefix(err.Error(), "parsing Cobertura XML:") {
		t.Fatalf("Parse error = %v", err)
	}
}

func TestCoverageGoProfileKeepsColonsInFilenamesAndReportsBadLineNumbers(t *testing.T) {
	p, err := coverage.Parse([]byte("mode: count\nC:/src/main.go:1.1,2.1 3 2\n"))
	if err != nil || p.Mode != "count" || len(p.Files) != 1 || p.Files[0].Path != "C:/src/main.go" {
		t.Fatalf("profile = %+v, err=%v", p, err)
	}
	_, err = coverage.Parse([]byte("mode: set\n1:bad\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("bad profile error = %v", err)
	}
}

func TestCoverageLCOVIgnoresOutsideRecordsAndTreatsUnreadableCountsAsMissed(t *testing.T) {
	p, err := coverage.Parse([]byte("DA:99,1\nSF:file.c\nDA:1,nope\nDA:2,1\nend_of_record\n"))
	if err != nil || len(p.Files) != 1 || p.Files[0].Statements != 2 || p.Files[0].Covered != 1 {
		t.Fatalf("profile = %+v, err=%v", p, err)
	}
}

func TestCoverageMergeKeepsLargestCountsCapsCoveredAndSortsPaths(t *testing.T) {
	got := coverage.Merge([]coverage.File{{Path: "z", Statements: 2, Covered: 4}, {Path: "a", Statements: 5, Covered: 2}, {Path: "a", Statements: 3, Covered: 4}})
	if len(got) != 2 || got[0].Path != "a" || got[0].Statements != 5 || got[0].Covered != 4 || got[1].Covered != 2 {
		t.Fatalf("merged files = %+v", got)
	}
}

func TestCoverageLocalizeRewritesInsideAndOutsideAbsolutePathsAndUnresolvedTails(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "x.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := coverage.Localize(root, []coverage.File{{Path: filepath.Join(root, "pkg", "x.go")}, {Path: "/outside/x.go"}, {Path: "mod/absent/x.go"}})
	if got[0].Path != "/outside/x.go" || got[1].Path != "mod/absent/x.go" || got[2].Path != "pkg/x.go" {
		t.Fatalf("localized files = %+v", got)
	}
}

func TestCoverageInstrumentationUnknownRunnerReturnsNoCommandArgumentsOrProfile(t *testing.T) {
	in := coverage.For(coverage.Options{Framework: "junit", Command: "mvn test", Dest: t.TempDir()})
	if in.Known || in.Command != "" || len(in.Args) != 0 || in.Profile != "" {
		t.Fatalf("instrumentation = %+v", in)
	}
}

func TestCoverageInstrumentationUsesFrameworkOverCommandAndQuotesMochaDestination(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "with space")
	in := coverage.For(coverage.Options{Framework: "mocha", Command: "npm test", Dest: dest})
	if !in.Known || !strings.Contains(in.Command, "npx nyc") || !strings.Contains(in.Command, "'"+strings.ReplaceAll(dest, "'", `\'`)+"'") || in.Format != coverage.FormatLCOV {
		t.Fatalf("instrumentation = %+v", in)
	}
}

func TestCoverageFindUsesTheFirstReportAndRejectsDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "coverage.out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "coverage"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "coverage", "lcov.info")
	if err := os.WriteFile(want, []byte("SF:x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := coverage.Find(root); !ok || got != want {
		t.Fatalf("Find = %q, %v", got, ok)
	}
}

func TestCoverageHistoryStatisticsRecentAndChangesIgnoreUnmeasuredFiles(t *testing.T) {
	h := &coverage.History{Version: 1}
	h.Add(coverage.Run{RanAt: time.Unix(2, 0), Files: []coverage.File{{Path: "a", Statements: 10, Covered: 5}}})
	h.Add(coverage.Run{RanAt: time.Unix(1, 0), Files: []coverage.File{{Path: "a", Statements: 10, Covered: 10}}})
	h.Add(coverage.Run{RanAt: time.Unix(3, 0)})
	if s := h.Stats(); s.Runs != 3 || s.Measured != 2 || s.Min != 50 || s.Max != 100 {
		t.Fatalf("stats = %+v", s)
	}
	if got := h.Recent(1); len(got) != 1 || !got[0].RanAt.Equal(time.Unix(2, 0).UTC()) {
		t.Fatalf("recent = %+v", got)
	}
	changes := coverage.Changes(h.Runs[0], h.Runs[1])
	if len(changes) != 1 || changes[0].Path != "a" || changes[0].PercentagePoints != -50 {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestCoverageHistoryAddNormalizesUTCAndMergesDuplicateFiles(t *testing.T) {
	h := &coverage.History{}
	at := time.Date(2026, 1, 1, 1, 0, 0, 0, time.FixedZone("x", 3600))
	h.Add(coverage.Run{RanAt: at, Files: []coverage.File{{Path: "b", Statements: 1, Covered: 1}, {Path: "b", Statements: 3, Covered: 2}}})
	if !h.Runs[0].RanAt.Equal(at.UTC()) || len(h.Runs[0].Files) != 1 || h.Runs[0].Files[0].Statements != 3 {
		t.Fatalf("run = %+v", h.Runs[0])
	}
}
