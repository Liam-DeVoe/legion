package graphcensus

// owesBlockIndex is true of the document whose uuid is the SQL expression artifact while it still
// owes its block index (document_block_versions.version null, as typedNodeOwed reads it): its
// document_blocks rows are not current, so what it holds is not known until the index catches up.
func owesBlockIndex(artifact string) string {
	return `exists (select 1 from document_block_versions owed where owed.artifact_id = ` + artifact + ` and owed.version is null)`
}
