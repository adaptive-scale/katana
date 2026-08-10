package update

import (
	"strings"
	"testing"
	"time"
)

// isolate points the check cache at a temporary directory and clears the
// environment that would otherwise disable checking on a developer machine or
// in CI.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("KATANA_CACHE_DIR", t.TempDir())
	t.Setenv("KATANA_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "")
}

func TestNoticeReportsCachedRelease(t *testing.T) {
	isolate(t)
	// A check from an hour ago is still fresh, so Start reports from the cache
	// without touching the network.
	if err := saveState(state{CheckedAt: time.Now().Add(-time.Hour), LatestTag: "v2.0.0"}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	Start("v1.0.0").Notice(&out)
	if !strings.Contains(out.String(), "v2.0.0") || !strings.Contains(out.String(), "katana update") {
		t.Errorf("notice = %q, want it to mention v2.0.0 and `katana update`", out.String())
	}

	out.Reset()
	Start("v2.0.0").Notice(&out)
	if out.String() != "" {
		t.Errorf("notice = %q, want silence when already current", out.String())
	}

	out.Reset()
	Start("v2.1.0").Notice(&out)
	if out.String() != "" {
		t.Errorf("notice = %q, want silence when ahead of the newest release", out.String())
	}
}

func TestStartStaysQuietWhenDisabled(t *testing.T) {
	isolate(t)
	if err := saveState(state{CheckedAt: time.Now(), LatestTag: "v2.0.0"}); err != nil {
		t.Fatal(err)
	}

	for _, env := range []string{"KATANA_NO_UPDATE_CHECK", "CI"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "1")
			var out strings.Builder
			Start("v1.0.0").Notice(&out)
			if out.String() != "" {
				t.Errorf("notice = %q, want silence when %s is set", out.String(), env)
			}
		})
	}

	// A locally built binary is ahead of the tag it names, so nagging it to
	// "update" would move it backwards.
	var out strings.Builder
	Start("v1.0.0-3-gabc1234").Notice(&out)
	if out.String() != "" {
		t.Errorf("notice = %q, want silence for a dev build", out.String())
	}
}

func TestNoticeToleratesMissingCache(t *testing.T) {
	isolate(t)
	t.Setenv("KATANA_NO_UPDATE_CHECK", "1") // keep the test off the network

	var out strings.Builder
	Start("v1.0.0").Notice(&out)
	if out.String() != "" {
		t.Errorf("notice = %q, want silence with no cached check", out.String())
	}
	if _, err := loadState(); err == nil {
		t.Error("loadState succeeded with no cache file written")
	}
}

func TestStateRoundTrips(t *testing.T) {
	isolate(t)
	want := state{CheckedAt: time.Now().Truncate(time.Second), LatestTag: "v3.1.4"}
	if err := saveState(want); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	got, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if got.LatestTag != want.LatestTag || !got.CheckedAt.Equal(want.CheckedAt) {
		t.Errorf("loadState = %+v, want %+v", got, want)
	}
}
