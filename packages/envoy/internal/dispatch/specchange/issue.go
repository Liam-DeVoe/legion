package specchange

import (
	"fmt"

	"github.com/sjawhar/envoy/internal/dispatch/pmdoc"
)

// VersionText is one version of a document of an issue, as it is stored: the document's address
// (its ref_key), the version's number, and its markdown.
type VersionText struct {
	Document string
	Version  int
	Markdown string
}

// FromVersions reads the change request of the issue issue from the versions a person's approval
// names: the statements and components among the typed blocks of statements, the version of the
// issue's spec, and the checks among those of checks, the version of its Verification document,
// nil for an issue that holds none. A version is read as it is stored, the bytes an approval of it
// covers (artifact_version_text_sha256 answers the digest of those bytes for a version that holds
// no record of a tree's revision, which at this build is every version), through the document
// parser and renderer every write goes through (pmdoc), so a block is read as its document holds it
// and a block inside code or a quote's text is none.
func FromVersions(issue string, statements VersionText, checks *VersionText) (Request, error) {
	blocks, err := typedBlocks(statements, func(typ string) bool { return IsSpecBlock(typ) && typ != KindCheck })
	if err != nil {
		return Request{}, err
	}
	if checks != nil {
		more, err := typedBlocks(*checks, func(typ string) bool { return typ == KindCheck })
		if err != nil {
			return Request{}, err
		}
		blocks = append(blocks, more...)
	}
	return FromBlocks(issue, blocks), nil
}

// typedBlocks is each block of version whose type take takes, in document order, with its address
// (`<document>#<block id>`) and its whole slice of the version's markdown.
func typedBlocks(version VersionText, take func(typ string) bool) ([]Block, error) {
	doc, err := pmdoc.ParseRendering(version.Markdown)
	if err != nil {
		return nil, fmt.Errorf("read version %d of %s: %w", version.Version, version.Document, err)
	}
	rendered, offsets, err := pmdoc.RenderWithBlockOffsets(doc)
	if err != nil {
		return nil, fmt.Errorf("read version %d of %s: %w", version.Version, version.Document, err)
	}
	var blocks []Block
	for _, offset := range offsets {
		if take(offset.Type) {
			blocks = append(blocks, Block{Address: version.Document + "#" + offset.ID, Text: rendered[offset.From:offset.To]})
		}
	}
	return blocks, nil
}
