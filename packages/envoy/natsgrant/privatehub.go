package natsgrant

// The names a hub rendered with private-project support and the auth service
// (internal/cipher/natscallout) agree on to admit a connection to that hub. One program writes each
// and another reads it, so both take it from here rather than spelling it: a hub's renderer
// (packages/envoy/infra/core) and the auth service already import this package for their grants.
// A package this one imports, internal/bus and internal/contracts among them, cannot import it
// back, so such a package takes the name from its caller. Nothing reads them yet: the auth
// service's private-hub admission, the hub writer's tag and the listener's required mode are
// later work.
const (
	// PrivateProjectsServerTag is the server_tags entry a hub's nats-server configuration carries
	// when, and only when, the hub is rendered with private-project support. nats-server copies a
	// server's tags into every authorization request it sends the auth service
	// (server/auth_callout.go, claim.Server.Tags), lowercased and trimmed as it reads them (jwt's
	// TagList.Add), so this value is lowercase and has no space to trim. The hub writer renders it;
	// the auth service reads a request carrying it as one from a hub rendered private, and applies
	// HubAdmissionAudience and PrivacyRequiredSuffix there. A hub whose tags, so normalized, do not
	// hold it reads as one rendered without private-project support: the auth service refuses
	// Dispatch's hub-admission proof there and requires no PrivacyRequiredSuffix of a listener,
	// whose other checks still apply.
	PrivateProjectsServerTag = "envoy-private-projects"

	// HubAdmissionAudience is the audience of the projected Kubernetes ServiceAccount token
	// Dispatch presents on its hub-admission proof connection: the pod that runs Dispatch projects
	// the token for it. The auth service admits a token of this audience only for Dispatch's own
	// account, and only on a request carrying PrivateProjectsServerTag. It must differ from
	// BROKER_NATS_K8S_AUDIENCE, at which the auth service admits a service's Kubernetes token on
	// every hub (PlatformTokens): a proof token projected for that audience is admitted as an
	// ordinary service connection whether or not the hub carries PrivateProjectsServerTag, and one
	// projected for an audience the auth service takes no NATS token at is refused on every hub.
	HubAdmissionAudience = "envoy-k7"

	// PrivacyRequiredSuffix is the suffix a listener built in required privacy mode appends to its
	// NATS client name; its leading space keeps it a word of its own after the name. The listener
	// appends it to the name it passes bus.WithName (internal/bus's options set it as opts.Name).
	// nats-server copies that name unchanged into the authorization request
	// (server/auth_callout.go, ConnectOptions.Name in fillConnectOpts and ClientInformation.Name
	// in fillClientInfo). On a request carrying PrivateProjectsServerTag the auth service refuses a
	// listener whose client name does not end with this exact suffix, whatever mode it runs in.
	PrivacyRequiredSuffix = " privacy-required"
)
