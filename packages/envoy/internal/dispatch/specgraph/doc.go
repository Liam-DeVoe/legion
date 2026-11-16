// Package specgraph reads the spec files of a repository, the *.md files directly in its
// .dispatch/spec directory. Parse reads the four blocks of those files - statements
// (requirement), checks (check), components (component) and terms (term) - with the one reader of
// a typed block's opening line, pmdoc.ParseTypedDirectiveOpen. Print writes a node as the block
// that Parse reads back as that node, and Digest names a node's content apart from the file and
// the line it is on.
//
// Validate reads a word as a name of code by its shape alone (CodeNames), so it refuses a plain
// word or a product name of that shape too: a slash between letters, as in I/O, and/or,
// client/server or a URL with a path; small letters and then a capital and a small letter in a
// word of letters and digits only, as in mmHg or ownCloud (but not mmHg-based); a small word, a
// dot and a capital, as in works.Then; a file ending, as in Three.js; and the form of a Go test's
// name, as in TestFlight. The writer says such a word in other words, such as "input and output"
// or "the client and the server". Validate splits a purpose into sentences only where ste-lint
// splits a line. So a word with a capital at the start of each part, such as ReadSet, a bare
// package word, such as pmdoc, and a sentence that ends in a closing quote, a closing parenthesis
// or an ellipsis, or that has no space after its stop, are for the reviewer of R25, who judges
// what no program decides (R49).
//
// The package is logic over bytes and values: it reaches no store, no route and no database
// (TestSpecgraphReachesNoStore), so the spec-check command, Dispatch's importer and the pilot can
// all call the same rules.
package specgraph
