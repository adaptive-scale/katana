package config

import "testing"

func TestIsSourcePath(t *testing.T) {
	cases := []struct {
		language string
		path     string
		want     bool
	}{
		{"go", "internal/app/service.go", true},
		{"golang", "main.GO", true},
		{"go", "README.md", false},
		{"typescript", "web/app.tsx", true},
		{"typescript", "web/app.js", false},
		{"javascript", "web/app.mjs", true},
		{"cobol", "payroll.cbl", false},
	}

	for _, c := range cases {
		if got := IsSourcePath(c.language, c.path); got != c.want {
			t.Errorf("IsSourcePath(%q, %q) = %v, want %v", c.language, c.path, got, c.want)
		}
	}
}

func TestIsTestPath(t *testing.T) {
	cases := []struct {
		name     string
		language string
		path     string
		want     bool
	}{
		{"go test file", "go", "internal/app/service_test.go", true},
		{"go source", "go", "internal/app/service.go", false},
		{"below a test directory", "go", "internal/tests/helper.go", true},
		{"below testdata", "python", "app/testdata/sample.py", true},
		{"python prefix", "python", "app/test_service.py", true},
		{"python suffix", "python", "app/service_test.py", true},
		{"python source", "python", "app/service.py", false},
		{"jest", "javascript", "web/cart.test.js", true},
		{"vitest spec", "typescript", "web/cart.spec.ts", true},
		{"junit", "java", "src/main/java/CartTest.java", true},
		{"rspec", "ruby", "lib/cart_spec.rb", true},
		{"xunit", "csharp", "src/CartTests.cs", true},
		// Case-sensitive, so an ordinary word ending in "test" is not a test.
		{"a word that ends in test", "java", "src/main/java/Latest.java", false},
		// A directory named "spec" holds tests, but a file called spec.rb does
		// not: the directory is what the convention is about.
		{"a source file called spec", "ruby", "lib/spec.rb", false},
		{"unknown language", "cobol", "payroll_test.cbl", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsTestPath(c.language, c.path); got != c.want {
				t.Errorf("IsTestPath(%q, %q) = %v, want %v", c.language, c.path, got, c.want)
			}
		})
	}
}

func TestExtensionsAreOwnedByTheCaller(t *testing.T) {
	got := Extensions("go")
	if len(got) == 0 || got[0] != ".go" {
		t.Fatalf("Extensions(\"go\") = %v", got)
	}
	got[0] = ".mutated"
	if again := Extensions("go"); again[0] != ".go" {
		t.Error("a caller editing the returned slice should not change katana's conventions")
	}
	if Extensions("cobol") != nil {
		t.Error("a language katana has no conventions for should have no extensions")
	}
}
