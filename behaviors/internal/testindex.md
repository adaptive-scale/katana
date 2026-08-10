# What this part of the product does

After katana asks an agent for a test file, this part reads the file's text and lists the test cases declared in it, so a behavior's tracker entry can say which tests came out of it rather than only which file was written. The reading is purely syntactic — nothing is compiled and no suite is run — and the rules are per language, preferring to miss an unusual declaration over listing a helper that is not a test.

## Listing names

- Given a file body and a language name, the result is the list of test case names declared in that body.
- Names appear in the order they are first seen in the body.
- A name that is declared more than once appears once in the list, at the position of its first appearance.
- Surrounding whitespace is removed from each name before it is listed, so a case described as `  adds two numbers  ` is listed as `adds two numbers`.
- A declaration whose captured name is empty, or is only whitespace, is not listed.
- An empty body produces an empty list.
- A language with no rules produces an empty list, not an error.
- The language name is matched after trimming and lowercasing, and the aliases `golang`, `py`, `js`, `node`, `nodejs`, `ts`, `rb`, `cs`, `c#`, `.net` and `dotnet` are accepted for Go, Python, JavaScript, TypeScript, Ruby and C# respectively.
- The languages with rules are Go, Python, JavaScript, TypeScript, Java, Kotlin, Ruby, Rust, C#, PHP and Swift; any other name yields an empty list.
- Blank lines declare nothing.
- Comment lines declare nothing.
- Where a language names its cases by a pattern on a single line, every match on that line is listed, so two cases declared on one line both appear.

## Markers and how far they reach

- Some languages mark a test with an annotation or attribute — such as `@Test`, `#[test]` or `[Fact]` — on the line before the declaration; the name taken is the one declared by the declaration the marker leads to.
- A marker applies to its own line first, so a marker and a declaration written on the same line still yield the declaration's name.
- A marker also applies to the next eight lines that are neither blank nor comments; a declaration on the ninth such line is not claimed by it.
- Blank lines and comment lines between a marker and its declaration do not count against those eight lines.
- A further line that is itself a marker restores the full reach, so a stack of annotations does not exhaust it.
- Once a marker has produced a name, it stops applying, so a later declaration is not claimed by the same marker.
- Marker syntax is removed from a line before the declaration is looked for, so an annotation carrying arguments is not mistaken for the declaration.
- A line that contains nothing but marker syntax yields no name and leaves the marker looking further down.
- While a marker is in reach, the name taken from a line is whatever that language's declaration rule finds there, whether or not the name looks like a test name.
- In PHP only, a marker written inside a doc comment counts and starts the marker's reach; in every other language a marker inside a comment is ignored.
- A comment line never uses up a marker's reach, even in PHP.

## Which lines count as comments

- In Go, Rust and Swift a line beginning with `//` is a comment.
- In Java, Kotlin, C# and JavaScript and TypeScript, a line beginning with `//`, `/*` or `*` is a comment.
- In Python and Ruby a line beginning with `#` is a comment.
- In PHP a line beginning with `#`, `//`, `/*` or `*` is a comment.
- A line beginning with `#[` is not treated as a comment, so PHP attributes and Rust attributes stay visible in languages where `#` otherwise opens a comment.

## Go

- A function whose name begins with `Test`, `Fuzz` or `Example` is listed.
- The `func` keyword must start the line with no leading whitespace, so an indented declaration is not listed.
- Only a name written directly after `func` is listed, so a method with a receiver in front of its name is not.
- `TestMain` is never listed, because it is the package entry point rather than a case.
- Go has no marker; annotations play no part.

## Python

- A function whose name begins with `test` is listed.
- The declaration may be indented.
- An `async def` declaration is listed the same way as a plain one.
- The name must begin with lowercase `test`, so a capitalised name is not listed.

## JavaScript and TypeScript

- The first argument of a call to `it` or `test` is listed as the case name.
- A suffixed form such as `it.each` or `test.concurrent` names its case the same way and is listed.
- The name may be written in single quotes, double quotes or backticks.
- The call must not be preceded directly by a word character, a `.` or a `$`, so a call like `suite.it('…')` or an identifier ending in `it` is not listed.
- A call with an empty string as its first argument yields no name.
- JavaScript and TypeScript have no marker; only the call form is recognised.

## Ruby

- A call to `it` at the start of a line, with a single- or double-quoted description, is listed under that description.
- The parentheses around the description are optional.
- A method whose name begins with `test_` is listed.
- A method named `testfoo`, without the underscore, is not listed.

## Java

- A line beginning with `@Test`, `@ParameterizedTest`, `@RepeatedTest`, `@TestFactory` or `@TestTemplate` is a marker.
- The marker is recognised whether or not the annotation carries arguments.
- The name taken is the identifier that sits directly in front of a parameter list, once annotations have been removed from the line, so modifiers and return types in front of it are ignored.

## Kotlin

- The same annotations as Java act as markers.
- A function name written in backticks is listed with its spaces and punctuation intact, so a case declared as a whole sentence keeps that sentence as its name.
- A plain function name followed by a parameter list is listed when no backticked name is present on the line.

## C#

- A line beginning with `[Fact`, `[Theory`, `[Test`, `[TestCase` or `[TestMethod` is a marker, with or without arguments.
- Bracketed attributes are removed from a line before the declaration is looked for.
- The name taken is the identifier directly in front of a parameter list.

## Rust

- An attribute line whose brackets contain the word `test` is a marker, so `#[test]`, `#[tokio::test]` and `#[cfg(test)]` all count.
- An attribute without the word `test`, such as `#[should_panic]`, is not a marker.
- Attributes are removed from a line before the declaration is looked for.
- The name taken is the identifier after `fn` and in front of a parameter list.

## PHP

- A method whose name begins with `test` is listed, with any combination of `public`, `protected`, `private`, `static`, `final` and `abstract` in front of it.
- The exact attribute `#[Test]`, or the docblock tag `@test`, marks a test whose name need not begin with `test`.
- The docblock tag is recognised when written after a leading `*` inside a doc comment.
- For a marked method, the name taken is the identifier after `function` and in front of a parameter list.
- A method matched both by its `test` prefix and by a marker is listed once.

## Swift

- A function whose name begins with `test` is listed.
- A line beginning with `@Test` is a marker, with or without arguments, so a case that drops the naming convention is still listed.
- For a marked function, the name taken is the identifier after `func` and in front of a parameter list.
