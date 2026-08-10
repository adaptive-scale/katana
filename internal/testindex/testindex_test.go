package testindex

import (
	"strings"
	"testing"
)

func TestNamesPerLanguage(t *testing.T) {
	cases := []struct {
		language string
		body     string
		want     []string
	}{
		{
			language: "go",
			body: `package tests

func helper(t *testing.T) {}

func TestMain(m *testing.M) {}

func TestAppliesDiscount(t *testing.T) {}

func TestRejectsExpiredCode(t *testing.T) {
	t.Run("subcase", func(t *testing.T) {})
}

func FuzzParseCode(f *testing.F) {}

// func TestCommentedOut(t *testing.T) {}
`,
			// TestMain runs the package, the helper is not a case, and a
			// commented-out test is not a test.
			want: []string{"TestAppliesDiscount", "TestRejectsExpiredCode", "FuzzParseCode"},
		},
		{
			language: "py",
			body: `import pytest

def helper():
    pass

def test_applies_discount():
    assert True

class TestCheckout:
    def test_rejects_expired_code(self):
        assert True

    async def test_async_path(self):
        assert True
`,
			want: []string{"test_applies_discount", "test_rejects_expired_code", "test_async_path"},
		},
		{
			language: "typescript",
			body: "import { describe, it, test } from 'vitest'\n" +
				"describe('checkout', () => {\n" +
				"  it('applies a discount', () => {})\n" +
				"  test(\"rejects an expired code\", () => {})\n" +
				"  it.each([1, 2])('ignored, the name is not first', n => {})\n" +
				"  it(`handles a template name`, () => {})\n" +
				"  // it('commented out', () => {})\n" +
				"})\n",
			want: []string{"applies a discount", "rejects an expired code", "handles a template name"},
		},
		{
			language: "java",
			body: `class CheckoutTest {
    private Cart cart() { return new Cart(); }

    @Test
    void appliesDiscount() {}

    @Test
    @DisplayName("rejects an expired code")
    public void rejectsExpiredCode() throws Exception {}

    @ParameterizedTest(name = "{0}")
    @ValueSource(strings = {"A", "B"})
    void acceptsKnownCodes(String code) {}

    void notATest() {}
}
`,
			// The helpers matter: the old display-only patterns indexed every
			// void method, which would have put cart() and notATest() in the
			// tracker as test cases.
			want: []string{"appliesDiscount", "rejectsExpiredCode", "acceptsKnownCodes"},
		},
		{
			language: "kotlin",
			body: "class CheckoutTest {\n" +
				"    @Test\n" +
				"    fun `applies a discount`() {}\n" +
				"\n" +
				"    @Test fun rejectsExpiredCode() {}\n" +
				"\n" +
				"    private fun helper() {}\n" +
				"}\n",
			want: []string{"applies a discount", "rejectsExpiredCode"},
		},
		{
			language: "rust",
			body: `fn helper() -> Cart { Cart::new() }

#[test]
fn applies_discount() {}

#[tokio::test]
async fn rejects_expired_code() {}

#[test]
#[should_panic]
fn panics_on_negative_total() {}
`,
			want: []string{"applies_discount", "rejects_expired_code", "panics_on_negative_total"},
		},
		{
			language: "csharp",
			body: `public class CheckoutTests
{
    private Cart Cart() => new Cart();

    [Fact]
    public void AppliesDiscount() {}

    [Theory]
    [InlineData("A")]
    public async Task RejectsExpiredCode(string code) {}
}
`,
			want: []string{"AppliesDiscount", "RejectsExpiredCode"},
		},
		{
			language: "php",
			body: `<?php
class CheckoutTest extends TestCase
{
    private function cart(): Cart {}

    public function testAppliesDiscount(): void {}

    #[Test]
    public function rejects_expired_code(): void {}

    /**
     * @test
     */
    public function accepts_a_known_code(): void {}
}
`,
			want: []string{"testAppliesDiscount", "rejects_expired_code", "accepts_a_known_code"},
		},
		{
			language: "ruby",
			body: `RSpec.describe Checkout do
  it 'applies a discount' do
  end

  it "rejects an expired code" do
  end

  def helper
  end
end
`,
			want: []string{"applies a discount", "rejects an expired code"},
		},
		{
			language: "swift",
			body: `final class CheckoutTests: XCTestCase {
    func makeCart() -> Cart { Cart() }

    func testAppliesDiscount() {}

    @Test
    func rejectsExpiredCode() async {}
}
`,
			want: []string{"testAppliesDiscount", "rejectsExpiredCode"},
		},
	}

	for _, c := range cases {
		t.Run(c.language, func(t *testing.T) {
			got := Names(c.body, c.language)
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("Names(%s) = %q, want %q", c.language, got, c.want)
			}
		})
	}
}

// TestNamesKeepsFileOrderWithoutRepeats is what makes the index worth storing:
// it reads like the file, and a name is listed once however often it is
// matched.
func TestNamesKeepsFileOrderWithoutRepeats(t *testing.T) {
	body := `package tests

func TestB(t *testing.T) {}
func TestA(t *testing.T) {}
func TestB(t *testing.T) {}
`
	got := Names(body, "go")
	if len(got) != 2 || got[0] != "TestB" || got[1] != "TestA" {
		t.Errorf("Names = %q, want [TestB TestA]", got)
	}
}

// TestNamesUnknownLanguageIsEmpty keeps an unrecognised language a shorter
// index rather than a failed generation.
func TestNamesUnknownLanguageIsEmpty(t *testing.T) {
	if got := Names("defmodule CartTest do\n  test \"works\" do\n  end\nend\n", "elixir"); got != nil {
		t.Errorf("Names = %q, want none", got)
	}
}

// TestMarkerDoesNotReachAcrossTheFile bounds the damage from a marker katana
// cannot pair with a declaration: it must not adopt an unrelated function
// twenty lines later.
func TestMarkerDoesNotReachAcrossTheFile(t *testing.T) {
	var b strings.Builder
	b.WriteString("#[test]\n")
	for i := 0; i < markerReach+2; i++ {
		b.WriteString("let x = 1;\n")
	}
	b.WriteString("fn not_a_test() {}\n")

	if got := Names(b.String(), "rust"); len(got) != 0 {
		t.Errorf("Names = %q, want none", got)
	}
}
