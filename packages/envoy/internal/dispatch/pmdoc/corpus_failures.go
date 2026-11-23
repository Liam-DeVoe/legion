package pmdoc

// CorpusFailures are the refusals of a document's parse that fail a corpus test of
// internal/dispatch (package corpustest, which every corpus test reads its corpus through) rather
// than leave the document out, each matched with errors.Is: a rendering of the document that reads
// back as another document, and one no reader reads. A corpus test leaves out a document the parse
// refuses for anything else - the step Dispatch runs to accept an upload refuses it too - and fails
// on these two, on a panic of the parse, and on every error of a later step. The list is kept here,
// in no test file, because the tests of package docs read it too, and pmdoc's own tests cannot
// import a package that imports pmdoc.
var CorpusFailures = []error{ErrMisreadRendering, ErrRenderingRefused}
