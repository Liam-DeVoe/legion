package graphstore

// The kinds of node that record how a piece of work was reasoned about (dispatch://INFRA-1950),
// as the graph addresses each: the one spelling of them that every writer of one of these nodes,
// and every read that names its kind, takes. An interaction is one run of the command bar, read
// by the person whose run it was. A lens is one analysis invoked on a node and logged under it,
// of the kind its lens_kind node names. A risk sits under its lens, and a constraint is what a
// requirement is held to. Each is addressed `dispatch://<kind>/<uuid>`, and a lens kind
// `dispatch://lens_kind/<slug>` (text.ParseRef), with no issue key in the address: where a node
// stands is its TuplePartOf tuple, so a move changes no address.
//
// A finding of a lens is no kind of its own. It is a node of kind and supertag block whose id
// names its lens, `lens/<uuid>#<block id>`, as a comment's body is blocks of the comment, and
// KindLensBlock is its address form: the from_kind graph_edges writes for it and the key the read
// registry knows it by (refs' nodeRegistry).
//
// A kind reaches the reads only in the commit that first writes a node of it, which registers
// its loader, its predicate and its backlink arms there: until then a read answers nothing of it,
// as it answers nothing of any kind this build does not know.
const (
	KindInteraction = "interaction"
	KindLens        = "lens"
	KindLensKind    = "lens_kind"
	KindConstraint  = "constraint"
	KindRisk        = "risk"
	KindLensBlock   = "lens_block"
)

// The tuple kinds those nodes are tied by, beside the TuplePartOf that places each under the node
// it stands under and the TupleAuthoredBy from each to whoever wrote it, where its credential
// proved an author.
//
// TupleChangedByRun points from each node a write made for a run created or changed to that run's
// interaction. TupleHasKind points from a lens to its lens_kind, and TupleTriggeredBy from a lens
// to the lens whose finding invoked it. TupleConstrainedBy points from a requirement block to a
// constraint it is held to, and TuplePreventedBy from a risk to each check block that covers it.
// graph_edges answers each of them from the tuples alone, with no projection row behind it
// (migration 20261116091932), and refs.TupleKinds lists them.
const (
	TupleChangedByRun  = "changed_by_run"
	TupleHasKind       = "has_kind"
	TupleTriggeredBy   = "triggered_by"
	TupleConstrainedBy = "constrained_by"
	TuplePreventedBy   = "prevented_by"
)
