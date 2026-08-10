// This file covers behaviors/internal/testindex.md: reading the text of a test
// file an agent has just written and listing the test cases declared in it, so
// a behavior's tracker entry can say which tests came out of it.
//
// Every assertion goes through testindex.Names, the one entry point the
// specification describes. Nothing here compiles a fixture or runs a suite —
// the specification is explicit that the reading is purely syntactic — so each
// body below is only as complete as the rule under test needs it to be.

package internal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/adaptive-scale/katana/internal/testindex"
)

// assertNames fails the test unless indexing body as language lists exactly
// want, in that order. Lengths are compared first so a miscount reports before
// an index panic; a nil result and an empty one both count as "nothing listed",
// which is the only distinction the specification draws.
func assertNames(t *testing.T, language, body string, want []string) {
	t.Helper()
	got := testindex.Names(body, language)
	if len(got) != len(want) {
		t.Fatalf("Names(%s) listed %d cases %q, want %d %q\nbody:\n%s", language, len(got), got, len(want), want, body)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names(%s)[%d] = %q, want %q\nbody:\n%s", language, i, got[i], want[i], body)
		}
	}
}

// assertOnly is assertNames for a body that declares a single case.
func assertOnly(t *testing.T, language, body, want string) {
	t.Helper()
	assertNames(t, language, body, []string{want})
}

// assertNothing fails the test unless indexing body as language lists no case.
func assertNothing(t *testing.T, language, body string) {
	t.Helper()
	assertNames(t, language, body, nil)
}

// javaFiller returns n Java lines that are neither blank nor comments and
// declare nothing, so each one uses up a line of a marker's reach.
func javaFiller(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "int filler%d = %d;\n", i, i)
	}
	return b.String()
}

// languageFixtures gives, for every language the specification says has rules,
// a body declaring one case and the name that must be listed for it.
var languageFixtures = []struct {
	language string
	body     string
	want     string
}{
	{"go", "func TestAppliesDiscount(t *testing.T) {}\n", "TestAppliesDiscount"},
	{"python", "def test_applies_discount():\n    assert True\n", "test_applies_discount"},
	{"javascript", "it('applies a discount', () => {})\n", "applies a discount"},
	{"typescript", "test('applies a discount', () => {})\n", "applies a discount"},
	{"java", "@Test\npublic void appliesADiscount() {}\n", "appliesADiscount"},
	{"kotlin", "@Test\nfun appliesADiscount() {}\n", "appliesADiscount"},
	{"ruby", "it 'applies a discount' do\nend\n", "applies a discount"},
	{"rust", "#[test]\nfn applies_a_discount() {}\n", "applies_a_discount"},
	{"csharp", "[Fact]\npublic void AppliesADiscount() {}\n", "AppliesADiscount"},
	{"php", "public function testAppliesADiscount() {}\n", "testAppliesADiscount"},
	{"swift", "func testAppliesADiscount() {}\n", "testAppliesADiscount"},
}

// jsLanguages are the two languages the specification gives one shared rule.
var jsLanguages = []string{"javascript", "typescript"}

// --- Listing names ---------------------------------------------------------

func TestEveryLanguageWithRulesListsTheCasesDeclaredInABody(t *testing.T) {
	for _, f := range languageFixtures {
		t.Run(f.language, func(t *testing.T) {
			assertOnly(t, f.language, f.body, f.want)
		})
	}
}

func TestNamesAreListedInTheOrderTheyAreFirstSeen(t *testing.T) {
	body := "it('third to be written', () => {})\n" +
		"it('first alphabetically', () => {})\n" +
		"it('second alphabetically', () => {})\n"

	assertNames(t, "javascript", body, []string{
		"third to be written", "first alphabetically", "second alphabetically",
	})
}

func TestARepeatedNameIsListedOnceAtItsFirstAppearance(t *testing.T) {
	body := "func TestApplies(t *testing.T) {}\n" +
		"func TestOther(t *testing.T) {}\n" +
		"func TestApplies(t *testing.T) {}\n"

	assertNames(t, "go", body, []string{"TestApplies", "TestOther"})
}

func TestSurroundingWhitespaceIsRemovedFromEachName(t *testing.T) {
	assertOnly(t, "javascript", "it('  adds two numbers  ', () => {})\n", "adds two numbers")
}

func TestADeclarationWithAnEmptyNameIsNotListed(t *testing.T) {
	body := "it('', () => {})\n" +
		"it('has a name', () => {})\n"

	assertNames(t, "javascript", body, []string{"has a name"})
}

func TestADeclarationWithAWhitespaceOnlyNameIsNotListed(t *testing.T) {
	body := "it('   ', () => {})\n" +
		"it('has a name', () => {})\n"

	assertNames(t, "javascript", body, []string{"has a name"})
}

func TestAnEmptyBodyListsNothing(t *testing.T) {
	assertNothing(t, "go", "")
}

func TestALanguageWithNoRulesListsNothingRatherThanFailing(t *testing.T) {
	// Names reports no error of its own, so "not an error" means the unknown
	// language simply comes back with nothing listed.
	for _, language := range []string{"cobol", "elixir", "perl", "text", "", "   "} {
		t.Run(fmt.Sprintf("%q", language), func(t *testing.T) {
			assertNothing(t, language, "func TestAppliesDiscount(t *testing.T) {}\n")
		})
	}
}

func TestLanguageAliasesAreAccepted(t *testing.T) {
	canonical := map[string]struct{ body, want string }{}
	for _, f := range languageFixtures {
		canonical[f.language] = struct{ body, want string }{f.body, f.want}
	}

	for _, c := range []struct{ alias, language string }{
		{"golang", "go"},
		{"py", "python"},
		{"js", "javascript"},
		{"node", "javascript"},
		{"nodejs", "javascript"},
		{"ts", "typescript"},
		{"rb", "ruby"},
		{"cs", "csharp"},
		{"c#", "csharp"},
		{".net", "csharp"},
		{"dotnet", "csharp"},
	} {
		t.Run(c.alias, func(t *testing.T) {
			f := canonical[c.language]
			assertOnly(t, c.alias, f.body, f.want)
		})
	}
}

func TestTheLanguageNameIsMatchedAfterTrimmingAndLowercasing(t *testing.T) {
	for _, written := range []string{"  go  ", "GO", "Go", "\tgolang\n", "  C#  "} {
		t.Run(fmt.Sprintf("%q", written), func(t *testing.T) {
			// C# and Go both index a Go body as nothing but the Go one; each
			// written form below names one of those two languages, so the
			// fixture is chosen to suit.
			if strings.Contains(strings.ToLower(written), "c#") {
				assertOnly(t, written, "[Fact]\npublic void AppliesADiscount() {}\n", "AppliesADiscount")
				return
			}
			assertOnly(t, written, "func TestAppliesDiscount(t *testing.T) {}\n", "TestAppliesDiscount")
		})
	}
}

func TestBlankLinesDeclareNothing(t *testing.T) {
	assertNothing(t, "go", "\n\n   \n\t\n\n")
}

func TestBlankLinesBetweenDeclarationsAreSkippedOver(t *testing.T) {
	body := "\nfunc TestFirst(t *testing.T) {}\n\n\n\nfunc TestSecond(t *testing.T) {}\n\n"

	assertNames(t, "go", body, []string{"TestFirst", "TestSecond"})
}

func TestCommentLinesDeclareNothing(t *testing.T) {
	body := "// it('commented out', () => {})\n" +
		"/* it('block commented', () => {}) */\n" +
		" * it('doc commented', () => {})\n" +
		"it('the only real case', () => {})\n"

	assertNames(t, "javascript", body, []string{"the only real case"})
}

func TestEveryMatchOnASingleLineIsListed(t *testing.T) {
	// JavaScript is the language whose rule matches anywhere on a line, so two
	// cases written on one line must both come back.
	body := "it('first on the line', () => {}); it('second on the line', () => {})\n"

	assertNames(t, "javascript", body, []string{"first on the line", "second on the line"})
}

// --- Markers and how far they reach ---------------------------------------

func TestAMarkerNamesTheDeclarationItLeadsTo(t *testing.T) {
	for _, c := range []struct{ language, body, want string }{
		{"java", "@Test\npublic void appliesADiscount() {}\n", "appliesADiscount"},
		{"rust", "#[test]\nfn applies_a_discount() {}\n", "applies_a_discount"},
		{"csharp", "[Fact]\npublic void AppliesADiscount() {}\n", "AppliesADiscount"},
	} {
		t.Run(c.language, func(t *testing.T) {
			assertOnly(t, c.language, c.body, c.want)
		})
	}
}

func TestAMarkerAppliesToItsOwnLineFirst(t *testing.T) {
	for _, c := range []struct{ language, body, want string }{
		{"java", "@Test public void appliesADiscount() {}\n", "appliesADiscount"},
		{"rust", "#[test] fn applies_a_discount() {}\n", "applies_a_discount"},
		{"csharp", "[Fact] public void AppliesADiscount() {}\n", "AppliesADiscount"},
	} {
		t.Run(c.language, func(t *testing.T) {
			assertOnly(t, c.language, c.body, c.want)
		})
	}
}

func TestAMarkerStillAppliesOnTheEighthLineItReaches(t *testing.T) {
	body := "@Test\n" + javaFiller(7) + "public void claimedByTheMarker() {}\n"

	assertOnly(t, "java", body, "claimedByTheMarker")
}

func TestAMarkerNoLongerAppliesOnTheNinthLineItReaches(t *testing.T) {
	body := "@Test\n" + javaFiller(8) + "public void tooFarFromTheMarker() {}\n"

	assertNothing(t, "java", body)
}

func TestBlankAndCommentLinesDoNotUseUpAMarkersReach(t *testing.T) {
	// Twelve inert lines — more than the eight a marker reaches — none of which
	// may count against it.
	body := "@Test\n" + strings.Repeat("\n// still looking for the declaration\n", 6) +
		"public void claimedByTheMarker() {}\n"

	assertOnly(t, "java", body, "claimedByTheMarker")
}

func TestAFurtherMarkerRestoresTheFullReach(t *testing.T) {
	// Fifteen declaring lines separate the first marker from the declaration,
	// so only the second marker resetting the reach can pair them up.
	body := "@Test\n" + javaFiller(7) + "@RepeatedTest(3)\n" + javaFiller(7) +
		"public void claimedByTheSecondMarker() {}\n"

	assertOnly(t, "java", body, "claimedByTheSecondMarker")
}

func TestAMarkerStopsApplyingOnceItHasProducedAName(t *testing.T) {
	body := "@Test\n" +
		"public void theMarkedCase() {}\n" +
		"public void aPlainHelper() {}\n"

	assertNames(t, "java", body, []string{"theMarkedCase"})
}

func TestMarkerSyntaxIsRemovedBeforeTheDeclarationIsLookedFor(t *testing.T) {
	// Without the annotation coming off first, "ParameterizedTest" sits in
	// front of a parameter list and would be taken for the case name.
	body := "@ParameterizedTest(name = \"case {0}\")\n" +
		"@ValueSource(ints = {1, 2, 3})\n" +
		"public void handlesEachValue(int n) {}\n"

	assertOnly(t, "java", body, "handlesEachValue")
}

func TestALineOfOnlyMarkerSyntaxYieldsNoNameAndKeepsTheMarkerLooking(t *testing.T) {
	body := "@Test\npublic void theDeclarationBelow() {}\n"

	assertNames(t, "java", body, []string{"theDeclarationBelow"})
}

func TestAMarkedDeclarationIsListedWhateverItsNameLooksLike(t *testing.T) {
	// Swift's own rule wants a "test" prefix; under a marker the name is
	// whatever the declaration rule finds.
	assertOnly(t, "swift", "@Test\nfunc checkoutFlow() {}\n", "checkoutFlow")
}

func TestAMarkerInsideADocCommentCountsInPHP(t *testing.T) {
	body := "/**\n" +
		" * @test\n" +
		" */\n" +
		"public function itAppliesADiscount() {}\n"

	assertOnly(t, "php", body, "itAppliesADiscount")
}

func TestAMarkerInsideADocCommentIsIgnoredOutsidePHP(t *testing.T) {
	body := "/**\n" +
		" * @Test\n" +
		" */\n" +
		"public void notClaimedByADocComment() {}\n"

	assertNothing(t, "java", body)
}

func TestACommentLineNeverUsesUpAMarkersReachInPHP(t *testing.T) {
	body := "#[Test]\n" + strings.Repeat("// a comment that must not count\n", 12) +
		"public function itAppliesADiscount() {}\n"

	assertOnly(t, "php", body, "itAppliesADiscount")
}

// --- Which lines count as comments ----------------------------------------

func TestGoRustAndSwiftTreatADoubleSlashLineAsAComment(t *testing.T) {
	for _, c := range []struct{ language, body, want string }{
		// Go has no marker, so a commented-out declaration is the observable case.
		{"go", "// func TestCommentedOut(t *testing.T) {}\nfunc TestReal(t *testing.T) {}\n", "TestReal"},
		{"rust", "#[test]\n// fn helper_in_a_comment() {}\nfn adds_two_numbers() {}\n", "adds_two_numbers"},
		{"swift", "@Test\n// func helperInAComment() {}\nfunc checkout() {}\n", "checkout"},
	} {
		t.Run(c.language, func(t *testing.T) {
			assertOnly(t, c.language, c.body, c.want)
		})
	}
}

func TestJavaKotlinCSharpAndJavaScriptTreatSlashAndStarLinesAsComments(t *testing.T) {
	for _, c := range []struct{ language, body, want string }{
		{"java", "@Test\n// void inALineComment() {}\n/* void inABlockComment() {} */\n * void inADocComment() {}\npublic void theRealCase() {}\n", "theRealCase"},
		{"kotlin", "@Test\n// fun inALineComment() {}\n/* fun inABlockComment() {} */\n * fun inADocComment() {}\nfun theRealCase() {}\n", "theRealCase"},
		{"csharp", "[Fact]\n// void InALineComment() {}\n/* void InABlockComment() {} */\n * void InADocComment() {}\npublic void TheRealCase() {}\n", "TheRealCase"},
		{"javascript", "// it('line', () => {})\n/* it('block', () => {}) */\n * it('doc', () => {})\nit('the real case', () => {})\n", "the real case"},
		{"typescript", "// it('line', () => {})\n/* it('block', () => {}) */\n * it('doc', () => {})\nit('the real case', () => {})\n", "the real case"},
	} {
		t.Run(c.language, func(t *testing.T) {
			assertOnly(t, c.language, c.body, c.want)
		})
	}
}

func TestPythonAndRubyTreatAHashLineAsAComment(t *testing.T) {
	for _, c := range []struct{ language, body, want string }{
		{"python", "# def test_commented_out():\ndef test_real():\n    assert True\n", "test_real"},
		{"ruby", "# it 'commented out' do\n# def test_commented_out\nit 'the real case' do\nend\n", "the real case"},
	} {
		t.Run(c.language, func(t *testing.T) {
			assertOnly(t, c.language, c.body, c.want)
		})
	}
}

func TestPHPTreatsHashSlashAndStarLinesAsComments(t *testing.T) {
	body := "# public function testHashed() {}\n" +
		"// public function testSlashed() {}\n" +
		"/* public function testBlocked() {} */\n" +
		" * public function testStarred() {}\n" +
		"public function testTheRealCase() {}\n"

	assertOnly(t, "php", body, "testTheRealCase")
}

func TestALineBeginningWithHashBracketIsNotAComment(t *testing.T) {
	for _, c := range []struct{ language, body, want string }{
		{"php", "#[Test]\npublic function itAppliesADiscount() {}\n", "itAppliesADiscount"},
		{"rust", "#[test]\nfn applies_a_discount() {}\n", "applies_a_discount"},
	} {
		t.Run(c.language, func(t *testing.T) {
			assertOnly(t, c.language, c.body, c.want)
		})
	}
}

// --- Go --------------------------------------------------------------------

func TestGoListsTestFuzzAndExampleFunctions(t *testing.T) {
	body := "func TestAppliesDiscount(t *testing.T) {}\n" +
		"func FuzzParseCode(f *testing.F) {}\n" +
		"func ExampleParse() {}\n" +
		"func helper(t *testing.T) {}\n"

	assertNames(t, "go", body, []string{"TestAppliesDiscount", "FuzzParseCode", "ExampleParse"})
}

func TestGoIgnoresAnIndentedFuncDeclaration(t *testing.T) {
	body := "\tfunc TestIndented(t *testing.T) {}\n    func TestAlsoIndented(t *testing.T) {}\n"

	assertNothing(t, "go", body)
}

func TestGoIgnoresAFuncWithAReceiverInFrontOfItsName(t *testing.T) {
	assertNothing(t, "go", "func (s *Suite) TestMethod(t *testing.T) {}\n")
}

func TestGoNeverListsTestMain(t *testing.T) {
	body := "func TestMain(m *testing.M) { os.Exit(m.Run()) }\n" +
		"func TestAppliesDiscount(t *testing.T) {}\n"

	assertNames(t, "go", body, []string{"TestAppliesDiscount"})
}

func TestGoHasNoMarker(t *testing.T) {
	assertNothing(t, "go", "@Test\nfunc helper(t *testing.T) {}\n")
}

// --- Python ----------------------------------------------------------------

func TestPythonListsFunctionsNamedWithATestPrefix(t *testing.T) {
	body := "def helper():\n    pass\n\ndef test_applies_discount():\n    assert True\n"

	assertNames(t, "python", body, []string{"test_applies_discount"})
}

func TestPythonListsAnIndentedDeclaration(t *testing.T) {
	body := "class TestCheckout:\n    def test_rejects_expired_code(self):\n        assert True\n"

	assertOnly(t, "python", body, "test_rejects_expired_code")
}

func TestPythonListsAnAsyncDeclarationTheSameWay(t *testing.T) {
	assertOnly(t, "python", "async def test_async_path():\n    assert True\n", "test_async_path")
}

func TestPythonIgnoresACapitalisedTestName(t *testing.T) {
	assertNothing(t, "python", "def Test_applies_discount():\n    assert True\n")
}

// --- JavaScript and TypeScript --------------------------------------------

func TestTheFirstArgumentOfItAndTestIsTheCaseName(t *testing.T) {
	for _, language := range jsLanguages {
		t.Run(language, func(t *testing.T) {
			body := "describe('checkout', () => {\n" +
				"  it('applies a discount', () => {})\n" +
				"  test('rejects an expired code', () => {})\n" +
				"})\n"

			assertNames(t, language, body, []string{"applies a discount", "rejects an expired code"})
		})
	}
}

func TestASuffixedCallFormNamesItsCaseTheSameWay(t *testing.T) {
	// The specification says a suffixed form names its case "the same way", so
	// each fixture writes the name as the first argument of the suffixed call.
	for _, language := range jsLanguages {
		t.Run(language, func(t *testing.T) {
			for _, call := range []string{"it.each", "it.only", "it.skip", "test.concurrent", "test.only"} {
				t.Run(call, func(t *testing.T) {
					assertOnly(t, language, call+"('applies a discount', () => {})\n", "applies a discount")
				})
			}
		})
	}
}

func TestACaseNameMayBeSingleDoubleOrBacktickQuoted(t *testing.T) {
	for _, language := range jsLanguages {
		t.Run(language, func(t *testing.T) {
			body := "it('single quoted', () => {})\n" +
				"it(\"double quoted\", () => {})\n" +
				"it(`backtick quoted`, () => {})\n"

			assertNames(t, language, body, []string{"single quoted", "double quoted", "backtick quoted"})
		})
	}
}

func TestACallPrecededByAWordCharacterDotOrDollarIsNotListed(t *testing.T) {
	for _, language := range jsLanguages {
		t.Run(language, func(t *testing.T) {
			for _, call := range []string{"suite.it", "visit", "$it", "unit"} {
				t.Run(call, func(t *testing.T) {
					assertNothing(t, language, call+"('not a case', () => {})\n")
				})
			}
		})
	}
}

func TestACallWithAnEmptyFirstArgumentYieldsNoName(t *testing.T) {
	for _, language := range jsLanguages {
		t.Run(language, func(t *testing.T) {
			assertNothing(t, language, "it('', () => {})\ntest(\"\", () => {})\n")
		})
	}
}

func TestJavaScriptAndTypeScriptHaveNoMarker(t *testing.T) {
	for _, language := range jsLanguages {
		t.Run(language, func(t *testing.T) {
			assertNothing(t, language, "@Test\nfunction shouldApplyADiscount() {}\n")
		})
	}
}

// --- Ruby ------------------------------------------------------------------

func TestRubyListsAnItDescriptionAtTheStartOfALine(t *testing.T) {
	body := "describe Checkout do\n" +
		"  it 'applies a discount' do\n" +
		"  end\n" +
		"end\n"

	assertOnly(t, "ruby", body, "applies a discount")
}

func TestRubyAcceptsAnItDescriptionWithOrWithoutParentheses(t *testing.T) {
	for _, c := range []struct{ name, line string }{
		{"without parentheses", "it 'applies a discount' do\n"},
		{"with parentheses", "it('applies a discount') do\n"},
		{"double quoted", "it \"applies a discount\" do\n"},
		{"parenthesised and double quoted", "it(\"applies a discount\") do\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertOnly(t, "ruby", c.line, "applies a discount")
		})
	}
}

func TestRubyListsMethodsNamedWithATestUnderscorePrefix(t *testing.T) {
	body := "def test_applies_discount\nend\n\ndef helper\nend\n"

	assertNames(t, "ruby", body, []string{"test_applies_discount"})
}

func TestRubyIgnoresAMethodNamedTestWithoutAnUnderscore(t *testing.T) {
	assertNothing(t, "ruby", "def testfoo\nend\n")
}

// --- Java ------------------------------------------------------------------

func TestJavaMarkerAnnotations(t *testing.T) {
	for _, annotation := range []string{"@Test", "@ParameterizedTest", "@RepeatedTest", "@TestFactory", "@TestTemplate"} {
		t.Run(annotation, func(t *testing.T) {
			assertOnly(t, "java", annotation+"\npublic void appliesADiscount() {}\n", "appliesADiscount")
		})
	}
}

func TestJavaMarkerAnnotationsAreRecognisedWithArguments(t *testing.T) {
	for _, annotation := range []string{"@Test(expected = IllegalStateException.class)", "@RepeatedTest(3)", "@ParameterizedTest(name = \"run {0}\")"} {
		t.Run(annotation, func(t *testing.T) {
			assertOnly(t, "java", annotation+"\npublic void appliesADiscount() {}\n", "appliesADiscount")
		})
	}
}

func TestJavaTakesTheIdentifierInFrontOfTheParameterList(t *testing.T) {
	for _, c := range []struct{ name, declaration, want string }{
		{"modifiers and a void return", "public static final void appliesADiscount() {", "appliesADiscount"},
		{"a generic return type", "public List<String> namesOfEveryDiscount() {", "namesOfEveryDiscount"},
		{"parameters of its own", "void rejectsAnExpiredCode(String code, int amount) {", "rejectsAnExpiredCode"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertOnly(t, "java", "@Test\n"+c.declaration+"\n}\n", c.want)
		})
	}
}

// --- Kotlin ----------------------------------------------------------------

func TestKotlinUsesTheSameMarkerAnnotationsAsJava(t *testing.T) {
	for _, annotation := range []string{"@Test", "@ParameterizedTest", "@RepeatedTest", "@TestFactory", "@TestTemplate"} {
		t.Run(annotation, func(t *testing.T) {
			assertOnly(t, "kotlin", annotation+"\nfun appliesADiscount() {}\n", "appliesADiscount")
		})
	}
}

func TestKotlinKeepsABacktickedNameIntact(t *testing.T) {
	body := "@Test\nfun `applies a discount to the total`() {\n}\n"

	assertOnly(t, "kotlin", body, "applies a discount to the total")
}

func TestKotlinListsAPlainFunctionNameWhenThereIsNoBacktickedOne(t *testing.T) {
	assertOnly(t, "kotlin", "@Test\nfun appliesADiscount(): Unit {\n}\n", "appliesADiscount")
}

// --- C# --------------------------------------------------------------------

func TestCSharpMarkerAttributes(t *testing.T) {
	for _, attribute := range []string{"[Fact]", "[Theory]", "[Test]", "[TestCase]", "[TestMethod]"} {
		t.Run(attribute, func(t *testing.T) {
			assertOnly(t, "csharp", attribute+"\npublic void AppliesADiscount() {}\n", "AppliesADiscount")
		})
	}
}

func TestCSharpMarkerAttributesAreRecognisedWithArguments(t *testing.T) {
	for _, attribute := range []string{"[Fact(DisplayName = \"applies a discount\")]", "[TestCase(1, 2)]", "[Theory(Skip = \"flaky\")]"} {
		t.Run(attribute, func(t *testing.T) {
			assertOnly(t, "csharp", attribute+"\npublic void AppliesADiscount() {}\n", "AppliesADiscount")
		})
	}
}

func TestCSharpRemovesBracketedAttributesBeforeLookingForTheDeclaration(t *testing.T) {
	// Without the attribute coming off first, "InlineData" sits in front of a
	// parameter list and would be taken for the case name.
	body := "[Theory]\n" +
		"[InlineData(1, 2)]\n" +
		"public void AddsTwoNumbers(int a, int b) { }\n"

	assertOnly(t, "csharp", body, "AddsTwoNumbers")
}

func TestCSharpTakesTheIdentifierInFrontOfTheParameterList(t *testing.T) {
	body := "[Fact]\npublic async Task AppliesADiscountAsync() { }\n"

	assertOnly(t, "csharp", body, "AppliesADiscountAsync")
}

// --- Rust ------------------------------------------------------------------

func TestRustTreatsAnAttributeContainingTheWordTestAsAMarker(t *testing.T) {
	for _, attribute := range []string{"#[test]", "#[tokio::test]", "#[cfg(test)]"} {
		t.Run(attribute, func(t *testing.T) {
			assertOnly(t, "rust", attribute+"\nfn applies_a_discount() {}\n", "applies_a_discount")
		})
	}
}

func TestRustIgnoresAnAttributeWithoutTheWordTest(t *testing.T) {
	assertNothing(t, "rust", "#[should_panic]\nfn not_a_test() {}\n")
}

func TestRustRemovesAttributesBeforeLookingForTheDeclaration(t *testing.T) {
	body := "#[test]\n" +
		"#[should_panic(expected = \"boom\")]\n" +
		"fn panics_loudly() {}\n"

	assertOnly(t, "rust", body, "panics_loudly")
}

func TestRustTakesTheIdentifierAfterFn(t *testing.T) {
	body := "#[tokio::test]\nasync fn awaits_the_result() -> Result<(), Error> {\n}\n"

	assertOnly(t, "rust", body, "awaits_the_result")
}

// --- PHP -------------------------------------------------------------------

func TestPHPListsMethodsNamedWithATestPrefix(t *testing.T) {
	body := "public function testAppliesADiscount() {}\n" +
		"public function helper() {}\n"

	assertNames(t, "php", body, []string{"testAppliesADiscount"})
}

func TestPHPAcceptsAnyCombinationOfModifiersInFrontOfAMethod(t *testing.T) {
	for _, modifiers := range []string{
		"", "public ", "protected ", "private ", "static ", "final ", "abstract ",
		"public static ", "final public ", "private static final ",
	} {
		t.Run(fmt.Sprintf("%q", modifiers), func(t *testing.T) {
			assertOnly(t, "php", modifiers+"function testAppliesADiscount() {}\n", "testAppliesADiscount")
		})
	}
}

func TestPHPAttributeMarksAMethodWhateverItsName(t *testing.T) {
	body := "#[Test]\npublic function it_applies_a_discount(): void {}\n"

	assertOnly(t, "php", body, "it_applies_a_discount")
}

func TestPHPDocblockTagMarksAMethodWhateverItsName(t *testing.T) {
	body := "/**\n" +
		" * Checks the discount.\n" +
		" *\n" +
		" * @test\n" +
		" */\n" +
		"public function it_applies_a_discount(): void {}\n"

	assertOnly(t, "php", body, "it_applies_a_discount")
}

func TestPHPIgnoresAnAttributeThatIsNotExactlyTest(t *testing.T) {
	assertNothing(t, "php", "#[Group('slow')]\npublic function itIsNotMarked() {}\n")
}

func TestPHPListsAMethodMatchedByBothItsPrefixAndAMarkerOnce(t *testing.T) {
	body := "#[Test]\npublic function testAppliesADiscount() {}\n"

	assertNames(t, "php", body, []string{"testAppliesADiscount"})
}

// --- Swift -----------------------------------------------------------------

func TestSwiftListsFunctionsNamedWithATestPrefix(t *testing.T) {
	body := "final class CheckoutTests: XCTestCase {\n" +
		"    func testAppliesADiscount() {}\n" +
		"    func helper() {}\n" +
		"}\n"

	assertNames(t, "swift", body, []string{"testAppliesADiscount"})
}

func TestSwiftMarkerListsAFunctionThatDropsTheNamingConvention(t *testing.T) {
	assertOnly(t, "swift", "@Test\nfunc checkoutFlow() {}\n", "checkoutFlow")
}

func TestSwiftMarkerIsRecognisedWithArguments(t *testing.T) {
	body := "@Test(\"applies a discount\")\nfunc checkoutFlow() async throws {\n}\n"

	// The name taken is the identifier after `func`, not the display string the
	// macro carries.
	assertOnly(t, "swift", body, "checkoutFlow")
}
