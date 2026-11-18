/**
 * Go's white space, the set Dispatch trims a value by (`strings.TrimSpace`): the characters Go's
 * `unicode.IsSpace` reports - `\t`, `\n`, `\v`, `\f`, `\r`, a space, U+0085 and U+00A0, and the
 * Unicode `White_Space` property above Latin-1, which is U+1680, U+2000 to U+200A, U+2028,
 * U+2029, U+202F, U+205F and U+3000 (`IsSpace` in Go's `unicode/graphic.go`, reading the
 * `_White_Space` table of its `unicode/tables.go`), as the contents of a character class.
 *
 * The set is written out because it is Go's and not JavaScript's: walked over every Unicode
 * scalar, `String.prototype.trim` and `strings.TrimSpace` differ on exactly two characters.
 * `trim` removes U+FEFF, which Go keeps, so `trim` calls U+FEFF blank where the server reads
 * content; Go removes U+0085, which `trim` keeps, so `trim` calls U+0085 content where the
 * server reads blank. This is the one place that pair is written out, and the lines that turn
 * on it cite this comment rather than repeating it.
 *
 * `packages/envoy/internal/dispatch/text/refs.go` records a split of the same class in another
 * pair of sets ("The whitespace is spelled out rather than written `\s`, which means another
 * set in each language: JavaScript's also matches a vertical tab and U+FEFF"). That pair is the
 * two languages' `\s`, and it differs in a vertical tab and U+FEFF, not in the two characters
 * above.
 *
 * `packages/contracts/fixtures/go-unicode-isspace.json` holds the 25 scalars `unicode.IsSpace`
 * reports. `goBlankPattern` is held to that list over every scalar by `goBlankPattern` > `calls
 * blank exactly the scalars the committed list holds`, and Go's own predicate by
 * TestUnicodeIsSpaceReportsTheCommittedScalars in
 * `packages/envoy/internal/dispatch/text/whitespace_fixture_test.go`: a character either side
 * gains or loses reddens its test, and the list and this set are corrected together.
 */
const GO_SPACE = String.raw`\t\n\v\f\r \u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000`;

/** Text Go's `strings.TrimSpace` empties: no characters at all, or only Go's white space. */
export const goBlankPattern = new RegExp(`^[${GO_SPACE}]*$`, "u");

/** Go's white space at either end of a text, which `strings.TrimSpace` takes away. */
export const goSpaceAtEnds = new RegExp(`^[${GO_SPACE}]+|[${GO_SPACE}]+$`, "gu");

/**
 * A value that names nothing in a URL path: Go's white space alone, or, inside such white space,
 * only dots and slashes. Empty, it leaves an empty step (`POST /api/v1/comments//resolve`); `.`
 * and `..` are steps the URL parser drops (`/api/v1/agents/../answers` is `/api/v1/answers`);
 * and `./`, `../`, `.//` or `/` is what a script writes for `"./$X"` with an empty `X`, which a
 * server that reads the path decoded takes as no step or a step up.
 */
export const pathNamesNothingPattern = new RegExp(`^[${GO_SPACE}]*[./]*[${GO_SPACE}]*$`, "u");
