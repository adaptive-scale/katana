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
}

var languages = map[string]languageDefaults{
	"go":         {"go-test", "{snake}_test.go", "go test ./..."},
	"python":     {"pytest", "test_{snake}.py", "pytest"},
	"javascript": {"jest", "{snake}.test.js", "npm test"},
	"typescript": {"vitest", "{snake}.test.ts", "npm test"},
	"java":       {"junit5", "{Name}Test.java", "mvn test"},
	"kotlin":     {"junit5", "{Name}Test.kt", "gradle test"},
	"ruby":       {"rspec", "{snake}_spec.rb", "bundle exec rspec"},
	"rust":       {"cargo-test", "{snake}_test.rs", "cargo test"},
	"csharp":     {"xunit", "{Name}Tests.cs", "dotnet test"},
	"php":        {"phpunit", "{Name}Test.php", "vendor/bin/phpunit"},
	"swift":      {"xctest", "{Name}Tests.swift", "swift test"},
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
	if d, ok := languages[normalizeLanguage(language)]; ok {
		return d.framework
	}
	return ""
}

// DefaultOutputTemplate returns the conventional generated-file name template.
func DefaultOutputTemplate(language string) string {
	if d, ok := languages[normalizeLanguage(language)]; ok {
		return d.outputTemplate
	}
	return "{snake}_test.txt"
}

// DefaultTestCommand returns the conventional command for running the suite.
func DefaultTestCommand(language string) string {
	if d, ok := languages[normalizeLanguage(language)]; ok {
		return d.testCommand
	}
	return ""
}

func normalizeLanguage(l string) string {
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
