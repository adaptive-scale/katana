package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/coverage"
	"github.com/adaptive-scale/katana/internal/history"
	"github.com/adaptive-scale/katana/internal/report"
	"github.com/adaptive-scale/katana/internal/results"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func TestAge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		ts   time.Time
		want string
	}{
		{"never generated", time.Time{}, "-"},
		{"seconds", now.Add(-3 * time.Second), "just now"},
		{"minutes", now.Add(-90 * time.Second), "1m ago"},
		{"hours", now.Add(-5 * time.Hour), "5h ago"},
		{"days", now.Add(-3 * 24 * time.Hour), "3d ago"},
		{"older than a week", now.Add(-30 * 24 * time.Hour), now.Add(-30 * 24 * time.Hour).Local().Format("2006-01-02")},
		{"in the future", now.Add(time.Hour), now.Add(time.Hour).Local().Format("2006-01-02")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Age(c.ts); got != c.want {
				t.Errorf("Age(%v) = %q, want %q", c.ts, got, c.want)
			}
		})
	}
}

// TestStatusColours pins the one thing a reader takes from the colour: only a
// behavior that needs nothing done is green, and every other state is not.
func TestStatusColours(t *testing.T) {
	if StatusStyle(tracker.StatusUpToDate) != Green {
		t.Error("an up-to-date behavior should be green")
	}
	for _, s := range []tracker.Status{
		tracker.StatusNew,
		tracker.StatusBehaviorChanged,
		tracker.StatusOutputMissing,
		tracker.StatusOutputModified,
		tracker.StatusConfigChanged,
		tracker.StatusOutputUntracked,
	} {
		if StatusStyle(s) == Green {
			t.Errorf("%v is not a state that needs nothing done, so it must not be green", s)
		}
	}
}

func TestPassedText(t *testing.T) {
	p := Printer{on: true}
	cases := map[string]struct {
		tally results.Tally
		want  Style
		text  string
	}{
		"all passed":   {results.Tally{Pass: 3}, Green, "3/3"},
		"one failed":   {results.Tally{Pass: 2, Fail: 1}, Red, "2/3"},
		"not covered":  {results.Tally{Pass: 1, Unknown: 2}, Yellow, "1/3"},
		"nothing knew": {results.Tally{Unknown: 3}, Grey, "-"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := p.PassedText(c.tally)
			if Strip(got) != c.text {
				t.Errorf("PassedText = %q, want %q", Strip(got), c.text)
			}
			if !strings.Contains(got, "\x1b["+string(c.want)+"m") {
				t.Errorf("PassedText = %q, want it painted %v", got, c.want)
			}
		})
	}
}

// TestCaseMarkSeparatesUnrunFromFailed is the distinction the marks exist for:
// a case the last run never reached must not read as one that failed.
func TestCaseMarkSeparatesUnrunFromFailed(t *testing.T) {
	p := Plain()
	if got := p.CaseMark("", false); got != "•" {
		t.Errorf("an unrun case is a bullet, got %q", got)
	}
	if got := p.CaseMark(report.StatusFail, true); got != "✗" {
		t.Errorf("a failed case is a cross, got %q", got)
	}
	if got := p.CaseMark(report.StatusPass, true); got != "✓" {
		t.Errorf("a passed case is a tick, got %q", got)
	}
	if got := p.CaseMark(report.StatusSkip, true); got != "○" {
		t.Errorf("a skipped case is a circle, got %q", got)
	}
}

// TestBehaviorSparkPlotsOnlyCoveringRuns keeps the chart honest: a run that said
// nothing about a behavior is not a data point in that behavior's history.
func TestBehaviorSparkPlotsOnlyCoveringRuns(t *testing.T) {
	p := Plain()
	runs := []history.Run{
		{Behaviors: []history.Behavior{{Source: "a.md", Pass: 2}}},
		{Behaviors: []history.Behavior{{Source: "b.md", Pass: 1}}},
		{Behaviors: []history.Behavior{{Source: "a.md", Pass: 1, Fail: 1}}},
	}
	got := p.BehaviorSpark("a.md", runs)
	if n := len([]rune(got)); n != 2 {
		t.Errorf("BehaviorSpark drew %d columns, want 2: %q", n, got)
	}
	if []rune(got)[0] != '█' {
		t.Errorf("a run in which everything passed should be a full column: %q", got)
	}
}

func TestRunSparkColoursFailures(t *testing.T) {
	p := Printer{on: true}
	got := p.RunSpark([]history.Run{{Pass: 2}, {Pass: 1, Fail: 1}})
	if !strings.Contains(got, "\x1b[32m") {
		t.Errorf("a passing run should be green: %q", got)
	}
	if !strings.Contains(got, "\x1b[31m") {
		t.Errorf("a run with a failure should be red: %q", got)
	}
}

func TestCoverageSparkUsesCoverageHeightAndThresholdColours(t *testing.T) {
	p := Printer{on: true}
	got := p.CoverageSpark([]coverage.Run{
		{Files: []coverage.File{{Statements: 100, Covered: 25}}},
		{Files: []coverage.File{{Statements: 100, Covered: 60}}},
		{Files: []coverage.File{{Statements: 100, Covered: 90}}},
	})
	if len([]rune(Strip(got))) != 3 {
		t.Errorf("coverage spark = %q, want three columns", got)
	}
	for _, colour := range []string{"\x1b[31m", "\x1b[33m", "\x1b[32m"} {
		if !strings.Contains(got, colour) {
			t.Errorf("coverage spark is missing colour %q: %q", colour, got)
		}
	}
}
