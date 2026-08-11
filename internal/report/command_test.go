package report

import "testing"

// TestFilterNarrowsToOneBehavior covers what running a single behavior rests
// on: the command katana hands the shell must select that behavior's tests and
// nothing else.
func TestFilterNarrowsToOneBehavior(t *testing.T) {
	cases := []struct {
		name      string
		framework string
		command   string
		file      string
		tests     []string
		want      string
		narrowed  bool
	}{
		{
			name:      "go by test name",
			framework: "go-test",
			command:   "go test ./... -v",
			file:      "tests/checkout_test.go",
			tests:     []string{"TestCheckout", "TestTax"},
			want:      "go test ./... -v -run '^(TestCheckout|TestTax)$'",
			narrowed:  true,
		},
		{
			// `go test -run` matches subtests level by level, so an anchored
			// pattern holding a slash would select nothing at all.
			name:      "go subtests select their parent",
			framework: "go-test",
			command:   "go test ./...",
			tests:     []string{"TestCheckout/adds", "TestCheckout/removes"},
			want:      "go test ./... -run '^(TestCheckout)$'",
			narrowed:  true,
		},
		{
			name:      "pytest by file",
			framework: "pytest",
			command:   "pytest -v",
			file:      "tests/test_checkout.py",
			tests:     []string{"test_adds"},
			want:      "pytest -v tests/test_checkout.py",
			narrowed:  true,
		},
		{
			name:      "jest by file",
			framework: "jest",
			command:   "npx jest",
			file:      "tests/checkout.test.js",
			want:      "npx jest tests/checkout.test.js",
			narrowed:  true,
		},
		{
			// katana does not know how to narrow cargo, and running the wrong
			// tests would be worse than running all of them.
			name:      "unknown runner is left alone",
			framework: "cargo-test",
			command:   "cargo test",
			tests:     []string{"checkout"},
			want:      "cargo test",
		},
		{
			// A command with shell syntax in it is not one katana can append an
			// argument to and still know where the argument lands.
			name:      "shell syntax is left alone",
			framework: "go-test",
			command:   "go test ./... | tee out.log",
			tests:     []string{"TestCheckout"},
			want:      "go test ./... | tee out.log",
		},
		{
			name:      "nothing to select by",
			framework: "go-test",
			command:   "go test ./...",
			want:      "go test ./...",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, narrowed := Filter(c.framework, c.command, c.file, c.tests)
			if got != c.want {
				t.Errorf("Filter = %q, want %q", got, c.want)
			}
			if narrowed != c.narrowed {
				t.Errorf("narrowed = %v, want %v", narrowed, c.narrowed)
			}
		})
	}
}

// TestFilterRefusesUnsafeNames keeps katana from assembling a command line out
// of names it did not write. They come from a generated test file, and the
// command goes to a shell.
func TestFilterRefusesUnsafeNames(t *testing.T) {
	got, narrowed := Filter("go-test", "go test ./...", "", []string{"Test; rm -rf /"})
	if narrowed || got != "go test ./..." {
		t.Errorf("Filter = %q (narrowed %v), want the command untouched", got, narrowed)
	}
	got, narrowed = Filter("pytest", "pytest", "tests/$(whoami).py", nil)
	if narrowed || got != "pytest" {
		t.Errorf("Filter = %q (narrowed %v), want the command untouched", got, narrowed)
	}
}

func TestVerboseAddsPerCaseFlag(t *testing.T) {
	if got, added := Verbose("go-test", "go test ./..."); !added || got != "go test ./... -v" {
		t.Errorf("Verbose = %q (added %v)", got, added)
	}
	if got, added := Verbose("go-test", "go test ./... -v"); added || got != "go test ./... -v" {
		t.Errorf("an already-verbose command should be left alone, got %q", got)
	}
	if _, added := Verbose("cargo-test", "cargo test"); added {
		t.Error("katana should not invent a flag for a runner it does not know")
	}
}
