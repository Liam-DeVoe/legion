// packages/broker-client/src/__tests__/remote-words.test.ts
//
// The broker's words are its own, and they can quote a URL that carries a credential: a user and
// password in its user information, a token in a query or fragment value. A refusal prints them
// as the rule for remote text reads them (`redactRemoteText`), as the Go client's `RefusedError`
// does (packages/envoy/internal/brokerclient/remote_words_test.go): in its message, in
// `JSON.stringify`, in Bun's and Node's inspection, and in a registration still pending that
// quotes it.
//
// Each check is the designated assertion of cmd/redact-gate's mutation gate: it prints the output
// it read as `REDACT-CAPTURE <capture> <base64>` and fails with a message that begins
// `REDACT-ASSERT <capture>`.

import { expect, test } from "bun:test";
import { inspect } from "node:util";
import { WORDS_NOT_READABLE } from "@legion/contracts/remote-text";
import { newDpopKey } from "../dpop";
import type { TokenSource } from "../source";
import {
  BrokerRefusedError,
  type BrokerToken,
  BrokerTokenClient,
  RegistrationPendingError,
} from "../token-client";
import { SUBJECT_TOKEN_TYPE_KUBERNETES, TOKEN_TYPE_DPOP } from "../wire";
import { accepted, refusalOf, stubBroker } from "./stub";

const USER = "not-a-secret-user";
const PASSWORD = "not-a-secret-password";
const TOKEN = "not-a-secret-token";
/** The parameter OAuth carries an access token under, in two parts so no line of this file reads
 *  as a credential to a scanner. */
const PARAM = ["access", "token"].join("_");
const WORDS = `the issuer at https://${USER}:${PASSWORD}@issuer.example/path?${PARAM}=${TOKEN}`;
const SHOWN = `the issuer at https://issuer.example/path?${PARAM}=`;
const CODE = `https://${USER}:${PASSWORD}@issuer.example/code?${PARAM}=${TOKEN}`;
const CODE_SHOWN = `https://issuer.example/code?${PARAM}=`;

const DESCRIPTIONS = [
  { name: "user information and a query value", words: WORDS, shown: SHOWN },
  {
    name: "a fragment's value",
    words: `sign in again at https://issuer.example/callback#${PARAM}=${TOKEN}`,
    shown: `sign in again at https://issuer.example/callback#${PARAM}=`,
  },
  {
    name: "words the rule cannot read",
    words: `${WORDS} refused the subject token`,
    shown: WORDS_NOT_READABLE,
  },
] as const;

const SCRIPTED: TokenSource = {
  read: async () => ({ token: "projected", tokenType: SUBJECT_TOKEN_TYPE_KUBERNETES }),
  describe: () => "a scripted identity",
};

/** The designated assertion: capture text, then fail where it holds a secret. */
function requireNone(capture: string, text: string): void {
  console.log(`REDACT-CAPTURE ${capture} ${Buffer.from(text, "utf8").toString("base64")}`);
  for (const secret of [USER, PASSWORD, TOKEN]) {
    expect(
      text.includes(secret),
      `REDACT-ASSERT ${capture}: the output prints the secret ${secret}: ${text}`
    ).toBe(false);
  }
}

/** requireNone for every form of `error` a caller can print, under capture/<form>. */
function requireNoneFormatted(capture: string, error: Error): void {
  const forms: Record<string, string> = {
    message: error.message,
    String: String(error),
    "JSON.stringify": JSON.stringify(error),
    "JSON.stringify-held": JSON.stringify({ error }),
    "Bun.inspect": Bun.inspect(error),
    "util.inspect": inspect(error, { showHidden: true, depth: 5 }),
    stack: error.stack ?? "",
  };
  for (const [form, text] of Object.entries(forms)) requireNone(`${capture}/${form}`, text);
}

/** The refusal of one trade the broker answers with `status` and `body`. */
async function refusalFor(status: number, body: Record<string, unknown>): Promise<Error | null> {
  const broker = stubBroker(() => ({ status, body }));
  try {
    const client = new BrokerTokenClient({
      brokerUrl: broker.url,
      key: await newDpopKey(),
      subject: SCRIPTED,
    });
    return await refusalOf(() => client.token());
  } finally {
    broker.stop();
  }
}

const REFUSALS = [
  {
    name: "the code, with a description",
    code: CODE,
    description: "revoked",
    want: `HTTP 400 ${CODE_SHOWN}: revoked`,
  },
  {
    name: "the code, with no description",
    code: CODE,
    description: "",
    want: `HTTP 400 ${CODE_SHOWN}`,
  },
  ...DESCRIPTIONS.map((row) => ({
    name: `the description: ${row.name}`,
    code: "invalid_grant",
    description: row.words,
    want: `HTTP 400 invalid_grant: ${row.shown}`,
  })),
];

test.each(
  REFUSALS.map((row) => [row.name, row] as const)
)("a refusal prints the broker's words as the rule reads them: %s", async (_name, row) => {
  const refusal = await refusalFor(400, { error: row.code, error_description: row.description });
  expect(refusal).toBeInstanceOf(BrokerRefusedError);
  requireNoneFormatted("ts.BrokerRefusedError", refusal as Error);
  expect(refusal?.message).toBe(`the secrets broker refused a scripted identity: ${row.want}`);
  expect((refusal as BrokerRefusedError).hasCode(row.code)).toBe(true);
});

test.each(
  DESCRIPTIONS.map((row) => [row.name, row] as const)
)("a registration still pending prints the refusal's words as the rule reads them: %s", (_name, row) => {
  const refusal = new BrokerRefusedError(
    400,
    "invalid_grant",
    `pod_session_not_registered: ${row.words}`,
    "a pod's projected service-account token"
  );
  const pending = new RegistrationPendingError("session-1", refusal);
  requireNoneFormatted("ts.RegistrationPendingError", pending);
  expect(pending.message).toContain(
    row.shown === WORDS_NOT_READABLE
      ? `(${WORDS_NOT_READABLE})`
      : `(pod_session_not_registered: ${row.shown})`
  );
  expect(refusal.podSessionUnregistered).toBe(true);
});

test.each(
  DESCRIPTIONS.map((row) => [row.name, row] as const)
)("an answer's credential_expires_at prints as the rule reads it: %s", async (_name, row) => {
  const refusal = await refusalFor(200, {
    access_token: "a",
    token_type: "DPoP",
    expires_in: 60,
    issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
    credential_expires_at: row.words,
  });
  requireNone("ts.credential-expires-at", refusal?.message ?? "");
  expect(refusal?.message).toEndWith(
    `answered a credential_expires_at that is no RFC 3339 time (${JSON.stringify(row.shown)})`
  );
});

/** One trade at a stub broker whose accepted answer has `field` set to `value`. */
async function tradeWith(
  field: string,
  value: unknown
): Promise<{ token?: BrokerToken; error?: Error }> {
  const broker = stubBroker(() => ({
    status: 200,
    body: { ...accepted("broker-access-token", 600), [field]: value },
  }));
  try {
    const client = new BrokerTokenClient({
      brokerUrl: broker.url,
      key: await newDpopKey(),
      subject: SCRIPTED,
    });
    return await client.token().then(
      (token) => ({ token }),
      (error: unknown) => ({ error: error instanceof Error ? error : new Error(String(error)) })
    );
  } finally {
    broker.stop();
  }
}

test("a token type is compared as it came and kept as the closed DPoP", async () => {
  const { token, error } = await tradeWith("token_type", "dpop");
  expect(error).toBeUndefined();
  expect(token?.tokenType).toBe(TOKEN_TYPE_DPOP);
});

// `openid-client` refuses a token_type other than bearer and DPoP before this client reads the
// answer, so the token type that reaches `#read` and is refused there is Bearer.
test.each([
  ["token_type", "Bearer", `token_type ${JSON.stringify("Bearer")}`],
  ["credential_expires_at", DESCRIPTIONS[0].words, `(${JSON.stringify(DESCRIPTIONS[0].shown)})`],
  ["credential_expires_at", "soon", `(${JSON.stringify("soon")})`],
] as const)("an answer whose %s is not this grant's quotes it only as the rule reads it", async (field, value, quoted) => {
  const { token, error } = await tradeWith(field, value);
  expect(token).toBeUndefined();
  if (error === undefined) throw new Error(`${field} ${value} was taken`);
  requireNoneFormatted("ts.token-answer", error);
  expect(error?.message).toContain(quoted);
});

test("a credential expiry is parsed through the box", async () => {
  const { token, error } = await tradeWith("credential_expires_at", "2026-12-01T00:00:00Z");
  expect(error).toBeUndefined();
  expect(token?.credentialExpiresAt?.toISOString()).toBe("2026-12-01T00:00:00.000Z");
});
