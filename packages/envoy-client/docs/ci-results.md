# CI results: the tested commit and the authenticated run

A CI job writes a check from the spec files at its commit with:

```sh
agent-secrets result-grant -- dispatch result --project ACME \
  --check BUNDLE-c1 --verdict pass --commit "$GITHUB_SHA" \
  --observed 'the command or evidence the job actually ran'
```

The Go producer reads the job's signed GitHub identity and asks the broker for a short-lived results capability. The broker performs the existing OAuth exchange under a key generated inside its own process. It verifies that the resulting credential is results-only and bound to that key before it releases anything. The child receives `DISPATCH_RESULTS_TOKEN`, an airlock placeholder, and the normal proxy and certificate settings. It receives neither the Dispatch access token nor the DPoP private key.

The real airlock proxy substitutes the held token only at the configured Dispatch host and adds a fresh DPoP proof for the actual method and destination. A command-supplied proof or another access token cannot make it sign a different credential. A refused, expired or lost grant does not fall back to ordinary secret release or another identity. Grants belong to the issuing pod and last no longer than the token, introspection expiry or ten minutes, whichever ends first. A restarted or different pod refuses the old capability rather than silently changing its key.

## Job and broker configuration

The job needs `id-token: write` and the GitHub runner's `ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN`. Configure:

- `AGENT_SECRETS_URL`: the broker's base URL.
- `AGENT_SECRETS_MACHINE_IDENTITY=github-actions`.
- `AGENT_SECRETS_MACHINE_AUDIENCE`: the audience the broker requires for this workflow.
- `DISPATCH_URL`: the Dispatch HTTPS origin.

A job whose runner writes its signed identity to a file can instead set `AGENT_SECRETS_MACHINE_TOKEN_FILE`. The Go identity source rereads it; no TypeScript metadata client trades this identity. Do not put a bearer, signing key or session login in the command's arguments. A personal `DISPATCH_TOKEN` is not a results credential and cannot replace the placeholder.

The broker needs its result-workflow entry, the existing airlock proxy and certificate configuration, and a rule for the actual Dispatch host:

```json
{
  "secrets": [
    {"name": "DISPATCH_RESULTS_TOKEN", "kind": "dpop", "hosts": ["dispatch.example.com"]}
  ]
}
```

The rule carries the placeholder only in `Authorization`. It does not grant a project or choose a run: the broker derives those from the verified GitHub identity and its result-workflow entry. A workflow authorized only for ordinary credentials cannot obtain a results capability through this endpoint. Use HTTPS outside the isolated loopback test environment.

A failure to prove the job, trade its token or obtain the required scope runs no child command. The producer prints one safe diagnostic. A failure after the child sends its result follows the normal keyed-write rules: matching successful answers settle the call, a committed write with a lost answer is a landing, and an uncertain write must be read back before a new command uses another key.

## Stored statement and scope

`stored.attestation` is a bare, unsigned in-toto Statement v1 with predicate type `https://dispatch.dev/attestations/ci-check-result/v1`. The statement is the immutable result row in a standard envelope, not a claim of cryptographic signing. A later DSSE envelope would have to sign exactly this stored statement.

Dispatch derives the subject SHA, project, workflow, job, check run and run attempt from what GitHub and the broker authenticated. A request cannot substitute another commit or job. The recorder is the authenticated workflow run and session, **not** the person named in the broker's configuration; that person did not run the job and is not attributed as its recorder. Each accepted invocation appends a result. Retries of the same keyed write do not append it twice.

A results-only credential reaches no other Dispatch route. A normal session can read the stored results, but cannot write one.

## Real local verification

With a local Postgres server and Docker available, run:

```sh
DISPATCH_TEST_DATABASE_URL=postgres://… bun run --cwd packages/envoy-client ci-results
```

The stack prints complete `agent-secrets result-grant -- …` commands. It runs the real broker, producer, proxy and Dispatch, with a stand-in for GitHub and a TLS load balancer in an owned container. No fixed host port is claimed and no host network setting is changed. The tests assert the stored statement and its run-only recorder, duplicate-invocation behavior, wrong-commit and wrong-job refusals, forged issuer and unlisted-ref refusals, session-token restrictions, results-only route restrictions, and safe printing of hostile spec-file names.

To exercise the forged-issuer case separately:

```sh
bun run --cwd packages/envoy-client ci-results --forged
```

The token is signed by a key absent from the issuer's published key set. The real producer must be refused before the child writes anything.
