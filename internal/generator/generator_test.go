package generator

import (
	"strings"
	"testing"
)

func TestExtractCodePrefersLargestFence(t *testing.T) {
	stdout := "I'll write the tests.\n\n" +
		"```go\npackage tests\n\nfunc TestOne(t *testing.T) {}\nfunc TestTwo(t *testing.T) {}\n```\n\n" +
		"Here's a snippet I considered:\n\n```go\n// nope\n```\n"

	got := extractCode(stdout)
	if !strings.Contains(got, "TestOne") || !strings.Contains(got, "TestTwo") {
		t.Fatalf("largest block not chosen:\n%s", got)
	}
	if strings.Contains(got, "```") {
		t.Errorf("fences leaked into output:\n%s", got)
	}
	if strings.Contains(got, "I'll write") {
		t.Errorf("prose leaked into output:\n%s", got)
	}
}

func TestExtractCodeIgnoresConfirmationProse(t *testing.T) {
	// An agent that wrote the file itself replies with a sentence. Treating
	// that as a file body would produce a test file full of English.
	for _, stdout := range []string{
		"I've written the tests to tests/checkout_test.go.",
		"Done. Created tests/checkout_test.go with 6 cases.\nAll behaviors covered.\nLet me know if you want more.",
		"",
		"   \n\n ",
	} {
		if got := extractCode(stdout); got != "" {
			t.Errorf("extractCode(%q) = %q, want empty", stdout, got)
		}
	}
}

func TestExtractCodeAcceptsBareCode(t *testing.T) {
	stdout := `package tests

import "testing"

func TestDiscountReducesTotal(t *testing.T) {
	t.Fatal("not implemented")
}
`
	got := extractCode(stdout)
	if !strings.Contains(got, "func TestDiscountReducesTotal") {
		t.Errorf("bare code should pass through, got %q", got)
	}
}

func TestExtractCodeHandlesUnterminatedFence(t *testing.T) {
	stdout := "```python\ndef test_total():\n    assert True\n"
	got := extractCode(stdout)
	if !strings.Contains(got, "def test_total") {
		t.Errorf("truncated output should still yield its body, got %q", got)
	}
}

func TestBuildPromptCarriesTheEssentials(t *testing.T) {
	p := BuildPrompt(Request{
		BehaviorPath:      "behaviors/checkout.md",
		BehaviorContent:   "## Discounts\n- A valid code reduces the total.",
		OutputPath:        "tests/checkout_test.go",
		Language:          "go",
		Framework:         "go-test",
		ExtraInstructions: "Never call the payment gateway.",
	})

	for _, want := range []string{
		"tests/checkout_test.go",
		"behaviors/checkout.md",
		"go-test",
		"A valid code reduces the total.",
		"Never call the payment gateway.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(p, "Existing tests at that path") {
		t.Error("a new behavior should not claim to have existing tests")
	}
}

func TestBuildPromptAsksForAnUpdateWhenTestsExist(t *testing.T) {
	p := BuildPrompt(Request{
		BehaviorPath:    "behaviors/checkout.md",
		BehaviorContent: "## Discounts",
		OutputPath:      "tests/checkout_test.go",
		Language:        "go",
		Framework:       "go-test",
		ExistingTests:   "package tests\n\nfunc TestOld(t *testing.T) {}\n",
	})
	if !strings.Contains(p, "Existing tests at that path") {
		t.Error("existing tests should be shown to the harness")
	}
	if !strings.Contains(p, "TestOld") {
		t.Error("existing test body should be included so edits are preserved")
	}
}

func TestFenceEscapesNestedFences(t *testing.T) {
	// A behavior spec containing a code fence must not terminate the wrapper
	// early, or the harness sees a truncated specification.
	content := "Example:\n```go\nfoo()\n```\nEnd."
	wrapped := fence(content)
	if !strings.HasPrefix(wrapped, "````") {
		t.Errorf("wrapper should outgrow the nested fence, got:\n%s", wrapped)
	}
	if !strings.Contains(wrapped, "End.") {
		t.Error("content truncated")
	}
}
