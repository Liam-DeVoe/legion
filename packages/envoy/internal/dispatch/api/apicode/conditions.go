package apicode

// Condition names one condition a request can be refused for. A v2 route declares each refusal
// it answers as a Refusal, a (condition, code) pair, so one condition gets one code on every
// route (decision 4). Like a Code it is a struct with an unexported field, so no package but this
// one names a condition and a string does not convert to one.
type Condition struct {
	name string
}

// String is the condition's name, for a test's or a log's message.
func (c Condition) String() string { return c.name }

// Refusal is one refusal a v2 route declares: when Condition holds, the route answers Code.
type Refusal struct {
	Condition Condition
	Code      Code
}

// The objects a v2 path names, each missing: the path's {key}, {id}, {block_id}, {number},
// {attempt} or {owner}/{repo} names a row Dispatch stores, and that row does not exist
// (decision 11). A route answers each with the object's own not-found code.
var (
	IssueMissing             = Condition{"issue missing"}
	ProjectMissing           = Condition{"project missing"}
	DocumentMissing          = Condition{"document missing"}
	AskMissing               = Condition{"ask missing"}
	CommentMissing           = Condition{"comment missing"}
	MessageMissing           = Condition{"message missing"}
	HandoffMissing           = Condition{"handoff missing"}
	BroadcastMissing         = Condition{"broadcast missing"}
	PriorityMissing          = Condition{"priority missing"}
	ResultMissing            = Condition{"result missing"}
	AgentTokenMissing        = Condition{"agent token missing"}
	SessionGroupMissing      = Condition{"session group missing"}
	NearCopyCheckMissing     = Condition{"near-copy check missing"}
	GoingRequestMissing      = Condition{"going request missing"}
	BlockMissing             = Condition{"block missing"}
	VersionMissing           = Condition{"version missing"}
	DeliveryMissing          = Condition{"delivery missing"}
	RepositoryMissing        = Condition{"repository missing"}
	CredentialRequestMissing = Condition{"credential request missing"}
	CredentialGrantMissing   = Condition{"credential grant missing"}
	MachineLoginMissing      = Condition{"machine login missing"}
	SigninMissing            = Condition{"sign-in missing"}
)

// The conditions every v2 route can meet in its query or its loops (decision 5).
var (
	// PathUnknown is a path under /api/v2 that no route serves.
	PathUnknown = Condition{"path unknown"}
	// QueryKeyUndeclared is a query key the route does not declare.
	QueryKeyUndeclared = Condition{"query key undeclared"}
	// LimitOutOfBounds is a limit that is no integer within the route's bound.
	LimitOutOfBounds = Condition{"limit out of bounds"}
	// OffsetOutOfBounds is an offset that is no integer within the route's bound.
	OffsetOutOfBounds = Condition{"offset out of bounds"}
	// PositionMalformed is a position parameter that is not one of the route's position field.
	PositionMalformed = Condition{"position malformed"}
	// ParentLoop is a parent that would make an issue its own ancestor.
	ParentLoop = Condition{"parent loop"}
	// DependencyLoop is a dependency that would make an issue wait on itself.
	DependencyLoop = Condition{"dependency loop"}
)
