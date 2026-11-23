// The names of a request's values that a wire contract declares public: every other value a
// request carries, under any name, is a credential the words of its refusal must not print
// (`requestCredentials` here, `requestTokens` in Go). A name is here because the contract that
// defines it says its value is public, never because a test needs it, and each entry names that
// contract. A name nobody listed counts as a credential, so a new parameter is redacted until a
// contract declares it public. Go reads this list from
// packages/envoy/internal/redact/public_names_generated.go, which `bun run gen:go` writes from it
// and `bun run check:go` holds to it; `public-request-names.test.ts` and Go's
// public_names_test.go fail on an entry with no source, or on a name a credential travels under.

/** Where a request carries the value: a header, or a field of its query, form or JSON body. */
export type PublicNamePlace = "header" | "field";

/** One name whose value is public, where it is carried, and the contract that says so. */
export type PublicRequestName = {
  readonly place: PublicNamePlace;
  readonly name: string;
  readonly source: string;
};

export const PUBLIC_REQUEST_NAMES: readonly PublicRequestName[] = [
  { place: "header", name: "Accept", source: "RFC 9110 §12.5.1" },
  { place: "header", name: "Content-Length", source: "RFC 9110 §8.6" },
  { place: "header", name: "Content-Type", source: "RFC 9110 §8.3" },
  { place: "header", name: "User-Agent", source: "RFC 9110 §10.1.5" },
  {
    place: "header",
    name: "Metadata-Flavor",
    source:
      "Google Compute Engine metadata server: the header every request must carry, value Google",
  },
  { place: "field", name: "grant_type", source: "RFC 6749 §4.1.3, §6; RFC 8693 §2.1" },
  {
    place: "field",
    name: "client_id",
    source: "RFC 6749 §2.2: a client identifier is not a secret",
  },
  { place: "field", name: "scope", source: "RFC 6749 §3.3" },
  { place: "field", name: "subject_token_type", source: "RFC 8693 §2.1" },
  { place: "field", name: "actor_token_type", source: "RFC 8693 §2.1" },
  { place: "field", name: "requested_token_type", source: "RFC 8693 §2.1" },
  { place: "field", name: "audience", source: "RFC 8693 §2.1" },
  { place: "field", name: "resource", source: "RFC 8693 §2.1; RFC 8707 §2" },
  {
    place: "field",
    name: "session",
    source: "the broker's token exchange: cipherclient/testdata/wire.json, form.session",
  },
  {
    place: "field",
    name: "projects",
    source: "the broker's token exchange: cipherclient/testdata/wire.json, form.projects",
  },
  {
    place: "field",
    name: "launcher_credential",
    source:
      "the broker's token exchange: cipherclient/testdata/wire.json, form.launcher_credential",
  },
  {
    place: "field",
    name: "kind",
    source: "POST /v1/enrollments: the broker contract's enrollment kind",
  },
  {
    place: "field",
    name: "runtime_id",
    source: "POST /v1/enrollments: the broker contract's runtime the enrollment names",
  },
  {
    place: "field",
    name: "session_id",
    source: "POST /v1/enrollments: the broker contract's session the enrollment names",
  },
  {
    place: "field",
    name: "operator",
    source: "POST /v1/enrollments: the broker contract's operator, the person's sign-in",
  },
  {
    place: "field",
    name: "thumbprint",
    source: "POST /v1/enrollments: the RFC 7638 thumbprint of the session's public key",
  },
  {
    place: "field",
    name: "wait",
    source:
      "GET /v1/requests/{id}?wait=: the broker contract's hold on a pending request's read, in seconds",
  },
  {
    place: "field",
    name: "owner",
    source:
      "POST /v1/requests and POST /v1/grants/{id}/values: the broker contract's GitHub App narrowing, the owner a token is narrowed to",
  },
  {
    place: "field",
    name: "repositories",
    source:
      "POST /v1/requests and POST /v1/grants/{id}/values: the broker contract's GitHub App narrowing, the repositories a token is narrowed to",
  },
  {
    place: "field",
    name: "reason",
    source:
      "POST /v1/grants/{id}/values: the broker contract's release reason, which the broker records with the release",
  },
  {
    place: "field",
    name: "command",
    source:
      "POST /v1/grants/{id}/values: the broker contract's release command, which the broker records with the release",
  },
  {
    place: "field",
    name: "include",
    source:
      "GET /v1/sessions, /v1/roles/{role} and /v1/registry/{id}?include= (the Envoy listener): the registry states a read asks for",
  },
  {
    place: "field",
    name: "issue",
    source:
      "GET /v1/legion/agents?issue= (the Envoy listener): the Dispatch issue key the Legion agent registry is read for",
  },
  {
    place: "field",
    name: "topic",
    source:
      "POST /v1/messages/publish (the Envoy listener, listenerwire.PublishRequest): the topic a message is published to",
  },
  {
    place: "field",
    name: "dedupe_key",
    source:
      "POST /v1/messages/publish (the Envoy listener, listenerwire.MessageBody): the idempotency key the listener's stream recognises a repeat of the message under",
  },
  {
    place: "field",
    name: "message",
    source:
      "POST /v1/messages/send and /v1/messages/publish (the Envoy listener, listenerwire.MessageBody): the message text the listener delivers to its recipient",
  },
  {
    place: "field",
    name: "payload",
    source:
      "POST /v1/messages/send and /v1/messages/publish (the Envoy listener, listenerwire.MessageBody): the event frame, a string, the listener delivers beside the message; it carries the message's text again",
  },
  {
    place: "field",
    name: "target_session",
    source:
      'POST /v1/messages/send (the Envoy listener, listenerwire.SendRequest): the session id the message is sent to, which the listener names in its answer ("no live session <id>")',
  },
  {
    place: "field",
    name: "expects_reply",
    source:
      'POST /v1/messages/send and /v1/messages/publish (the Envoy listener, listenerwire.MessageBody): what the message wants back, false, "optional" or "required"',
  },
  {
    place: "field",
    name: "role",
    source:
      "POST /legion/v1/operator/claims/* (the Legion daemon's operator claim routes): the role a claim is spawned or read for",
  },
  {
    place: "field",
    name: "delegates",
    source:
      "IAM Service Account Credentials API, generateAccessToken: the service accounts of the chain",
  },
  {
    place: "field",
    name: "lifetime",
    source: "IAM Service Account Credentials API, generateAccessToken: the token's lifetime",
  },
  {
    place: "field",
    name: "page",
    source:
      "GitHub REST API, Using pagination in the REST API (docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api): the number of the page a list request reads",
  },
  {
    place: "field",
    name: "per_page",
    source:
      "GitHub REST API, Using pagination in the REST API (docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api): the number of results a page holds",
  },
];

/** Whether the value under `name`, carried at `place`, is public by its contract: a header's name
 *  compared without case, as HTTP compares it, a field's as written. */
export function isPublicRequestName(place: PublicNamePlace, name: string): boolean {
  return PUBLIC_REQUEST_NAMES.some(
    (entry) =>
      entry.place === place &&
      (place === "header" ? entry.name.toLowerCase() === name.toLowerCase() : entry.name === name)
  );
}
