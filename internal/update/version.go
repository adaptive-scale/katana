package update

import (
	"regexp"
	"strconv"
	"strings"
)

// Compare orders two version strings by semantic-version precedence and returns
// -1, 0 or 1. A leading "v" and any build metadata are ignored. Versions katana
// cannot parse — a bare commit sha from `git describe --always`, say — sort
// before every release, so an unstamped build is always considered behind.
func Compare(a, b string) int {
	va, aok := parse(a)
	vb, bok := parse(b)
	switch {
	case !aok && !bok:
		return 0
	case !aok:
		return -1
	case !bok:
		return 1
	}
	return va.compare(vb)
}

// devSuffix spots versions that `git describe` produced from a tree that is
// ahead of, or dirty relative to, the tag they name. Those builds are newer
// than the release they mention, so katana must not offer to "update" them
// back down to it.
var devSuffix = regexp.MustCompile(`(?:-\d+-g[0-9a-f]{4,}|-dirty)$`)

// IsDevBuild reports whether v identifies a locally built katana rather than a
// published release.
func IsDevBuild(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" {
		return true
	}
	if devSuffix.MatchString(v) {
		return true
	}
	_, ok := parse(v)
	return !ok
}

// semver is a parsed version: its dot-separated numbers and its prerelease
// identifiers, if any.
type semver struct {
	nums []int
	pre  []string
}

func parse(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	if s == "" {
		return semver{}, false
	}
	var nums []int
	for _, part := range strings.Split(s, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return semver{}, false
		}
		nums = append(nums, n)
	}
	return semver{nums: nums, pre: pre}, true
}

func (v semver) compare(o semver) int {
	for i := 0; i < max(len(v.nums), len(o.nums)); i++ {
		if c := cmp(at(v.nums, i), at(o.nums, i)); c != 0 {
			return c
		}
	}
	// A release outranks any of its prereleases: v1.2.0 > v1.2.0-rc.1.
	switch {
	case len(v.pre) == 0 && len(o.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1
	case len(o.pre) == 0:
		return -1
	}
	for i := 0; i < min(len(v.pre), len(o.pre)); i++ {
		if c := comparePre(v.pre[i], o.pre[i]); c != 0 {
			return c
		}
	}
	return cmp(len(v.pre), len(o.pre))
}

// comparePre orders two prerelease identifiers: numeric ones compare
// numerically and rank below alphanumeric ones, per the semver spec.
func comparePre(a, b string) int {
	na, aerr := strconv.Atoi(a)
	nb, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		return cmp(na, nb)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func at(s []int, i int) int {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func cmp(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
