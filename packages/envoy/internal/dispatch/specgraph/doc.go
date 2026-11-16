// Package specgraph reads the spec files of a repository, the *.md files directly in its
// .dispatch/spec directory. Parse reads the four blocks of those files - statements
// (requirement), checks (check), components (component) and terms (term) - with the one reader of
// a typed block's opening line, pmdoc.ParseTypedDirectiveOpen. Print writes a node as the block
// that Parse reads back as that node, and Digest names a node's content apart from the file and
// the line it is on.
//
// Validate refuses as code only the names that no plain word or product name has the shape of
// (CodeNames), so a word with a capital at the start of each part, such as ReadSet, and a bare
// package word, such as pmdoc, are for the reviewer of R25, who judges what no program decides
// (R49).
//
// The package is logic over bytes and values: it reaches no store, no route and no database
// (TestSpecgraphReachesNoStore), so the spec-check command, Dispatch's importer and the pilot can
// all call the same rules.
package specgraph
