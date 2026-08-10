// Package testindex lists the test cases declared in a generated test file.
//
// katana records the result in the tracker, so a behavior's entry says which
// tests came out of it and not merely which file it wrote. Reading is purely
// syntactic — katana has just asked an agent for a file, and neither compiling
// it nor running the suite is this package's job — so the rules are per
// language and err towards missing an unusual declaration rather than towards
// indexing a helper that is not a test at all.
package testindex

import (
	"regexp"
	"strings"

	"github.com/adaptive-scale/katana/internal/config"
)

// Names lists the test cases declared in body, in the order they appear and
// without repeats. A language katana has no rules for indexes as nothing: an
// empty index is a shorter listing, never an error.
func Names(body, language string) []string {
	r, ok := languages[config.NormalizeLanguage(language)]
	if !ok {
		return nil
	}

	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] || r.exclude[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	// A marker — @Test, #[test], [Fact] — says the declaration that follows is a
	// test. It usually sits on the line above, but further annotations can come
	// between, so it stays live for a few lines. The bound is what stops a
	// marker katana failed to pair up from claiming an unrelated function much
	// further down the file.
	reach := 0

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isComment(trimmed, r.comments) {
			// A comment declares nothing — but phpunit marks a test with a
			// docblock tag, so for those languages a marker still counts here.
			// Either way the line neither declares a case nor uses up the
			// marker's reach.
			if r.docMarker && r.marker.MatchString(line) {
				reach = markerReach
			}
			continue
		}

		for _, re := range r.direct {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				add(firstGroup(m))
			}
		}

		marked := r.marker != nil && r.marker.MatchString(line)
		if marked {
			reach = markerReach
		}
		if reach > 0 {
			// The marker's own syntax is dropped first: an annotation carrying
			// arguments looks exactly like the call this is scanning for.
			if name := declName(r, line); name != "" {
				add(name)
				reach = 0
			} else if !marked {
				reach--
			}
		}
	}
	return out
}

// markerReach is how many lines of annotations, attributes and modifiers a
// marker is allowed to look past before katana gives up on pairing it with a
// declaration.
const markerReach = 8

// rules describe how one language declares a test case.
type rules struct {
	// direct patterns capture a test case declared on a single line.
	direct []*regexp.Regexp
	// marker matches a line declaring that what follows is a test.
	marker *regexp.Regexp
	// decl captures the name of the declaration a marker applies to.
	decl []*regexp.Regexp
	// strip removes marker syntax before decl is applied.
	strip *regexp.Regexp
	// comments are the line prefixes that make a line inert.
	comments []string
	// docMarker is set when the language marks a test inside a doc comment, as
	// phpunit's @test tag does.
	docMarker bool
	// exclude drops names that match the shape of a test but are not one.
	exclude map[string]bool
}

var (
	// Annotation-driven languages share a shape: a marker line, then a method
	// declaration whose name is the identifier in front of the parameter list.
	jvmMarker = regexp.MustCompile(`^\s*@(?:Test|ParameterizedTest|RepeatedTest|TestFactory|TestTemplate)\b`)
	atStrip   = regexp.MustCompile(`@\w+(?:\s*\([^)]*\))?`)
	callDecl  = regexp.MustCompile(`\b(\w+)\s*\(`)

	slashComments = []string{"//", "/*", "*"}
	hashComments  = []string{"#"}
)

var languages = map[string]rules{
	"go": {
		direct: []*regexp.Regexp{
			regexp.MustCompile(`^func\s+((?:Test|Fuzz|Example)\w*)\s*\(`),
		},
		comments: []string{"//"},
		// TestMain is the package's entry point, not a case it contributes.
		exclude: map[string]bool{"TestMain": true},
	},
	"python": {
		direct: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:async\s+)?def\s+(test\w*)\s*\(`),
		},
		comments: hashComments,
	},
	"javascript": jsRules(),
	"typescript": jsRules(),
	"java": {
		marker:   jvmMarker,
		decl:     []*regexp.Regexp{callDecl},
		strip:    atStrip,
		comments: slashComments,
	},
	"kotlin": {
		marker: jvmMarker,
		decl: []*regexp.Regexp{
			// Kotlin test names are often whole sentences in backticks.
			regexp.MustCompile("fun\\s+`([^`]+)`"),
			regexp.MustCompile(`\bfun\s+(\w+)\s*\(`),
		},
		strip:    atStrip,
		comments: slashComments,
	},
	"ruby": {
		direct: []*regexp.Regexp{
			regexp.MustCompile(`^\s*it\s*\(?\s*(?:'([^']*)'|"([^"]*)")`),
			regexp.MustCompile(`^\s*def\s+(test_\w*)`),
		},
		comments: hashComments,
	},
	"rust": {
		marker:   regexp.MustCompile(`^\s*#\[[^\]]*\btest\b[^\]]*\]`),
		decl:     []*regexp.Regexp{regexp.MustCompile(`\bfn\s+(\w+)\s*\(`)},
		strip:    regexp.MustCompile(`#\[[^\]]*\]`),
		comments: []string{"//"},
	},
	"csharp": {
		marker:   regexp.MustCompile(`^\s*\[(?:Fact|Theory|Test|TestCase|TestMethod)\b`),
		decl:     []*regexp.Regexp{callDecl},
		strip:    regexp.MustCompile(`\[[^\]]*\]`),
		comments: slashComments,
	},
	"php": {
		direct: []*regexp.Regexp{
			regexp.MustCompile(`^\s*(?:(?:public|protected|private|static|final|abstract)\s+)*function\s+(test\w*)\s*\(`),
		},
		// A phpunit test may be named freely and marked instead, either with the
		// attribute or with the docblock tag it replaced.
		marker:    regexp.MustCompile(`^\s*(?:#\[Test\]|(?:\*\s*)?@test\b)`),
		decl:      []*regexp.Regexp{regexp.MustCompile(`\bfunction\s+(\w+)\s*\(`)},
		strip:     regexp.MustCompile(`#\[[^\]]*\]`),
		comments:  append([]string{"#"}, slashComments...),
		docMarker: true,
	},
	"swift": {
		direct: []*regexp.Regexp{
			regexp.MustCompile(`^\s*func\s+(test\w*)\s*\(`),
		},
		// Swift Testing drops the naming convention in favour of a macro.
		marker:   regexp.MustCompile(`^\s*@Test\b`),
		decl:     []*regexp.Regexp{regexp.MustCompile(`\bfunc\s+(\w+)\s*\(`)},
		strip:    atStrip,
		comments: []string{"//"},
	},
}

// jsRules covers jest, vitest and mocha, which all name a case in the first
// argument of it() or test(). Suffixed forms — it.each, test.concurrent — name
// theirs the same way.
func jsRules() rules {
	return rules{
		direct: []*regexp.Regexp{
			regexp.MustCompile("(?:^|[^\\w.$])(?:it|test)(?:\\.\\w+)*\\s*\\(\\s*(?:'([^']*)'|\"([^\"]*)\"|`([^`]*)`)"),
		},
		comments: slashComments,
	}
}

// declName returns the name declared on line, once the marker syntax that led
// katana here has been taken off it.
func declName(r rules, line string) string {
	if r.strip != nil {
		line = r.strip.ReplaceAllString(line, " ")
	}
	if strings.TrimSpace(line) == "" {
		return ""
	}
	for _, re := range r.decl {
		if m := re.FindStringSubmatch(line); m != nil {
			return firstGroup(m)
		}
	}
	return ""
}

// firstGroup returns the first non-empty capture, so one pattern can offer a
// name in several quotings.
func firstGroup(m []string) string {
	for _, g := range m[1:] {
		if g != "" {
			return g
		}
	}
	return ""
}

// isComment reports whether a line is inert. A leading "#" opens a comment in
// Python, Ruby and PHP — but "#[" opens an attribute in PHP and Rust, and those
// are the markers this package is looking for.
func isComment(trimmed string, prefixes []string) bool {
	for _, p := range prefixes {
		if !strings.HasPrefix(trimmed, p) {
			continue
		}
		if p == "#" && strings.HasPrefix(trimmed, "#[") {
			continue
		}
		return true
	}
	return false
}
