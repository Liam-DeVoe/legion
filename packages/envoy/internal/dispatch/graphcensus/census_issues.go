package graphcensus

// issueProjections are the census's kinds of the issues and the relations between them
// (projections). It holds none: the project of each issue's node is the project census's
// (ProjectCensus).
var issueProjections []projection

// issueBackfills are the backfill's passes over the issues and the relations between them
// (backfillPasses). It holds none: the node of each issue is the project pass's
// (backfillObjectNodes).
var issueBackfills []backfillPass
