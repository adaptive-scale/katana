package update

import "testing"

func TestCompareOrdersVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"1.2.3", "v1.2.3", 0},
		{"v1.2.3", "v1.2.4", -1},
		{"v1.10.0", "v1.9.0", 1},
		{"v2.0.0", "v1.99.99", 1},
		{"v1.2", "v1.2.0", 0},
		{"v1.2.3+build.5", "v1.2.3", 0},
		// A release outranks its prereleases, and prereleases order among
		// themselves numerically then alphabetically.
		{"v1.2.3-rc.1", "v1.2.3", -1},
		{"v1.2.3", "v1.2.3-rc.1", 1},
		{"v1.2.3-rc.1", "v1.2.3-rc.2", -1},
		{"v1.2.3-rc.2", "v1.2.3-rc.10", -1},
		{"v1.2.3-alpha", "v1.2.3-beta", -1},
		{"v1.2.3-rc", "v1.2.3-rc.1", -1},
		// Anything katana cannot parse sorts below every release.
		{"dev", "v0.0.1", -1},
		{"b86d2b1", "v1.0.0", -1},
		{"dev", "dev", 0},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsDevBuild(t *testing.T) {
	dev := []string{"", "dev", "b86d2b1", "v1.2.3-4-gb86d2b1", "v1.2.3-dirty", "v1.2.3-4-gb86d2b1-dirty"}
	for _, v := range dev {
		if !IsDevBuild(v) {
			t.Errorf("IsDevBuild(%q) = false, want true", v)
		}
	}
	released := []string{"v1.2.3", "1.2.3", "v1.2.3-rc.1", "v0.1.0"}
	for _, v := range released {
		if IsDevBuild(v) {
			t.Errorf("IsDevBuild(%q) = true, want false", v)
		}
	}
}
