package config

import (
	"path/filepath"
	"strings"
	"unicode"
)

// languageDefaults holds the conventions katana assumes for a language when the
// config does not say otherwise. Anything here is only a default — every field
// is overridable in katana.yaml.
type languageDefaults struct {
	framework      string
	outputTemplate string
	testCommand    string
	// extensions are the file suffixes `katana discover` reads as source in
	// this language.
	extensions []string
	// testStems mark a file as test code by the name it carries, once its
	// extension is off: "user_test.go" and "UserTest.java" both stem-match.
	// Matching is case-sensitive, so "Latest.java" is not a test.
	testPrefixes []string
	testSuffixes []string
}

var languages = map[string]languageDefaults{
	"go": {
		framework: "go-test", outputTemplate: "{snake}_test.go", testCommand: "go test ./...",
		extensions:   []string{".go"},
		testSuffixes: []string{"_test"},
	},
	"python": {
		framework: "pytest", outputTemplate: "test_{snake}.py", testCommand: "pytest",
		extensions:   []string{".py"},
		testPrefixes: []string{"test_"},
		testSuffixes: []string{"_test"},
	},
	"javascript": {
		framework: "jest", outputTemplate: "{snake}.test.js", testCommand: "npm test",
		extensions:   []string{".js", ".jsx", ".mjs", ".cjs"},
		testSuffixes: []string{".test", ".spec"},
	},
	"typescript": {
		framework: "vitest", outputTemplate: "{snake}.test.ts", testCommand: "npm test",
		extensions:   []string{".ts", ".tsx", ".mts", ".cts"},
		testSuffixes: []string{".test", ".spec"},
	},
	"java": {
		framework: "junit5", outputTemplate: "{Name}Test.java", testCommand: "mvn test",
		extensions:   []string{".java"},
		testSuffixes: []string{"Test", "Tests", "IT"},
	},
	"kotlin": {
		framework: "junit5", outputTemplate: "{Name}Test.kt", testCommand: "gradle test",
		extensions:   []string{".kt"},
		testSuffixes: []string{"Test", "Tests", "Spec"},
	},
	"ruby": {
		framework: "rspec", outputTemplate: "{snake}_spec.rb", testCommand: "bundle exec rspec",
		extensions:   []string{".rb"},
		testSuffixes: []string{"_spec", "_test"},
	},
	"rust": {
		framework: "cargo-test", outputTemplate: "{snake}_test.rs", testCommand: "cargo test",
		extensions:   []string{".rs"},
		testSuffixes: []string{"_test", "_tests"},
	},
	"csharp": {
		framework: "xunit", outputTemplate: "{Name}Tests.cs", testCommand: "dotnet test",
		extensions:   []string{".cs"},
		testSuffixes: []string{"Test", "Tests"},
	},
	"php": {
		framework: "phpunit", outputTemplate: "{Name}Test.php", testCommand: "vendor/bin/phpunit",
		extensions:   []string{".php"},
		testSuffixes: []string{"Test", "Tests"},
	},
	"swift": {
		framework: "xctest", outputTemplate: "{Name}Tests.swift", testCommand: "swift test",
		extensions:   []string{".swift"},
		testSuffixes: []string{"Test", "Tests"},
	},
}

// Languages returns the languages katana has built-in conventions for.
func Languages() []string {
	out := make([]string, 0, len(languages))
	for k := range languages {
		out = append(out, k)
	}
	return sortedStrings(out)
}

// DefaultFramework returns the conventional test framework for a language.
func DefaultFramework(language string) string {
	if d, ok := languages[NormalizeLanguage(language)]; ok {
		return d.framework
	}
	return ""
}

// DefaultOutputTemplate returns the conventional generated-file name template.
func DefaultOutputTemplate(language string) string {
	if d, ok := languages[NormalizeLanguage(language)]; ok {
		return d.outputTemplate
	}
	return "{snake}_test.txt"
}

// DefaultTestCommand returns the conventional command for running the suite.
func DefaultTestCommand(language string) string {
	if d, ok := languages[NormalizeLanguage(language)]; ok {
		return d.testCommand
	}
	return ""
}

// Extensions returns the source file extensions katana reads for a language,
// leading dot included. A language katana has no conventions for returns none,
// which is what stops `katana discover` from walking a repository blind.
func Extensions(language string) []string {
	d, ok := languages[NormalizeLanguage(language)]
	if !ok {
		return nil
	}
	return append([]string(nil), d.extensions...)
}

// IsSourcePath reports whether p is a source file in the given language.
func IsSourcePath(language, p string) bool {
	ext := filepath.Ext(p)
	for _, e := range Extensions(language) {
		if strings.EqualFold(ext, e) {
			return true
		}
	}
	return false
}

// testDirs are the directory names that hold test code in every language
// katana targets. A file below one of them is test code whatever it is called.
var testDirs = map[string]bool{
	"test": true, "tests": true, "spec": true, "specs": true,
	"__tests__": true, "testdata": true, "fixtures": true, "e2e": true,
}

// IsTestPath reports whether a source path is test code rather than the product
// code `katana discover` reads behavior from.
//
// Discovery describes what the product does; tests are a second telling of the
// same thing, and feeding them back in would document the test suite instead of
// the product. The check errs towards calling a file test code: a missed source
// file costs a paragraph of specification, while a test file mistaken for
// product code turns the assertions someone already wrote into a specification
// katana then generates assertions from.
func IsTestPath(language, p string) bool {
	p = filepath.ToSlash(p)
	segments := strings.Split(p, "/")
	for _, s := range segments[:max(len(segments)-1, 0)] {
		if testDirs[strings.ToLower(s)] {
			return true
		}
	}

	base := segments[len(segments)-1]
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	d, ok := languages[NormalizeLanguage(language)]
	if !ok {
		return false
	}
	for _, p := range d.testPrefixes {
		if strings.HasPrefix(stem, p) {
			return true
		}
	}
	for _, s := range d.testSuffixes {
		if strings.HasSuffix(stem, s) {
			return true
		}
	}
	return false
}

// NormalizeLanguage folds the spellings katana accepts for a language ("py",
// "golang") onto the one it keys its conventions by.
func NormalizeLanguage(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	switch l {
	case "js", "node", "nodejs":
		return "javascript"
	case "ts":
		return "typescript"
	case "py":
		return "python"
	case "rb":
		return "ruby"
	case "cs", "c#", ".net", "dotnet":
		return "csharp"
	case "golang":
		return "go"
	}
	return l
}

// renderTemplate fills {name}, {snake} and {Name} from a behavior file path.
// {name} is the base name with its extension stripped, as written.
func renderTemplate(tmpl, source string) string {
	base := filepath.Base(filepath.FromSlash(source))
	name := strings.TrimSuffix(base, filepath.Ext(base))

	r := strings.NewReplacer(
		"{name}", name,
		"{snake}", toSnake(name),
		"{Name}", toPascal(name),
	)
	return r.Replace(tmpl)
}

// toSnake converts "Checkout Flow", "checkout-flow" and "checkoutFlow" to
// "checkout_flow" so generated file names are stable across naming styles.
func toSnake(s string) string {
	var b strings.Builder
	prevLower := false
	for i, r := range s {
		switch {
		case r == '-' || r == ' ' || r == '.' || r == '_':
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
			prevLower = false
		case unicode.IsUpper(r):
			if i > 0 && prevLower && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			prevLower = false
		default:
			b.WriteRune(r)
			prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
		}
	}
	return strings.Trim(b.String(), "_")
}

// toPascal converts "checkout_flow" to "CheckoutFlow".
func toPascal(s string) string {
	parts := strings.FieldsFunc(toSnake(s), func(r rune) bool { return r == '_' })
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		runes := []rune(p)
		b.WriteRune(unicode.ToUpper(runes[0]))
		b.WriteString(string(runes[1:]))
	}
	return b.String()
}

func sortedStrings(in []string) []string {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
	return in
}
