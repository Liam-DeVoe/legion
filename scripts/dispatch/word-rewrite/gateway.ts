import { readFile } from "node:fs/promises";
import { setTimeout as sleep } from "node:timers/promises";

/**
 * The tool's own client of the deployment's model gateway, in the shape of
 * `scripts/approval-eval/models.ts`: `POST $ANTHROPIC_BASE_URL/v1/messages`, with the key read
 * from `/var/run/model-gateway-key/key` at each call (a key rolled between two calls is picked up
 * by the second) and `anthropic-version: 2023-06-01`. A transport error, a 429 and a 5xx are
 * tried again four times, with growing waits, jitter, and a `retry-after` that asks for longer. A
 * 200 whose body is not JSON, or carries no text, is reported once and not tried again. The key
 * never leaves this module: it goes into one header, and every fault text is scrubbed of every key
 * this process read. The judge's tests give it a fake `Model`; the gateway's own test gives this
 * client a fetch, a key file and a wait of its own (`GatewayIo`).
 */

/** What the judge asks of a model: one prompt, one text back, or null when none came. */
export interface Model {
  ask(prompt: { readonly system: string; readonly user: string }): Promise<string | null>;
}

export const DEFAULT_MODEL = "anthropic/claude-opus-5-5";

const KEY_FILE = "/var/run/model-gateway-key/key";
const MESSAGES_PATH = "/v1/messages";
const ANTHROPIC_VERSION = "2023-06-01";
const RETRY_WAITS_MS: readonly number[] = [2_000, 4_000, 8_000, 16_000];
const JITTER = 0.4;
const LONGEST_WAIT_MS = 60_000;
const TRY_TIME_MS = 180_000;
const MAX_TOKENS = 8_192;
const DETAIL_CHARS = 500;
const REDACTED = "<redacted>";

/** What a call reaches: the fetch it sends with, the file it reads the key from, and how it waits
 *  between tries. */
export interface GatewayIo {
  readonly fetch: (url: string, init: RequestInit) => Promise<Response>;
  readonly keyFile: string;
  readonly sleep: (ms: number) => Promise<unknown>;
}

const LIVE_IO: GatewayIo = {
  fetch: (input, init) => fetch(input, init),
  keyFile: KEY_FILE,
  sleep: (ms) => sleep(ms),
};

/** Every key this process read, so a fault text can be scrubbed of all of them. */
const keysRead = new Set<string>();

/** `text` with every key this process read taken out of it. */
function scrubKeys(text: string): string {
  let kept = text;
  for (const key of keysRead) kept = kept.split(key).join(REDACTED);
  return kept;
}

/** A gateway that cannot be called at all: no base URL, or no key. Its text names no key. */
class GatewayError extends Error {
  override readonly name = "GatewayError";
}

async function endpoint(keyFile: string): Promise<{ url: string; key: string }> {
  const base = process.env.ANTHROPIC_BASE_URL?.trim();
  if (!base)
    throw new GatewayError("ANTHROPIC_BASE_URL names the model gateway, and it is not set");
  let key: string;
  try {
    key = (await readFile(keyFile, "utf8")).replace(/[\r\n]+$/, "");
  } catch {
    throw new GatewayError(`${keyFile} could not be read, so no gateway call can be made`);
  }
  if (key === "") throw new GatewayError(`${keyFile} holds no key`);
  keysRead.add(key);
  return { url: `${base.replace(/\/+$/, "")}${MESSAGES_PATH}`, key };
}

function retryAfterMs(header: string | null): number {
  if (header === null) return 0;
  const seconds = Number(header.trim());
  if (Number.isFinite(seconds) && seconds >= 0) return Math.trunc(seconds * 1_000);
  const at = Date.parse(header);
  return Number.isNaN(at) ? 0 : Math.max(0, at - Date.now());
}

/** The text blocks of a Messages answer, joined; null when it carries none. */
function answerText(body: unknown): string | null {
  if (typeof body !== "object" || body === null || !("content" in body)) return null;
  const content = body.content;
  if (!Array.isArray(content)) return null;
  const texts = content.flatMap((block: unknown) =>
    typeof block === "object" &&
    block !== null &&
    "type" in block &&
    block.type === "text" &&
    "text" in block &&
    typeof block.text === "string"
      ? [block.text]
      : []
  );
  const joined = texts.join("\n");
  return joined.trim() === "" ? null : joined;
}

/** The text of a 200 answer, or null with its fault reported: a body that is not JSON, or that
 *  carries no text, is no fault that another try mends. */
function okText(body: string, fault: (detail: string) => void): string | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    fault(scrubKeys(`200 the answer is not JSON: ${body.slice(0, DETAIL_CHARS)}`));
    return null;
  }
  const text = answerText(parsed);
  if (text === null) fault("the answer carried no text");
  return text;
}

/** The gateway's client for `model`. A call that ends without an answer reports its scrubbed
 *  fault through `fault` and answers null. */
export function gatewayModel(
  model: string,
  fault: (detail: string) => void,
  io: GatewayIo = LIVE_IO
): Model {
  return {
    async ask(prompt) {
      const { url, key } = await endpoint(io.keyFile);
      const init = {
        method: "POST",
        headers: {
          "content-type": "application/json",
          "x-api-key": key,
          "anthropic-version": ANTHROPIC_VERSION,
        },
        body: JSON.stringify({
          model,
          max_tokens: MAX_TOKENS,
          system: prompt.system,
          messages: [{ role: "user", content: prompt.user }],
        }),
      };
      for (let attempt = 0; ; attempt += 1) {
        let again = true;
        let wait = 0;
        let detail = "";
        let answered: string | undefined;
        try {
          const response = await io.fetch(url, {
            ...init,
            signal: AbortSignal.timeout(TRY_TIME_MS),
          });
          const body = await response.text();
          if (response.ok) {
            answered = body;
          } else {
            again = response.status === 429 || response.status >= 500;
            wait = retryAfterMs(response.headers.get("retry-after"));
            detail = `${response.status} ${body.slice(0, DETAIL_CHARS)}`;
          }
        } catch (error) {
          detail = error instanceof Error ? `${error.name}: ${error.message}` : String(error);
        }
        if (answered !== undefined) return okText(answered, fault);
        const step = again ? RETRY_WAITS_MS[attempt] : undefined;
        if (step === undefined) {
          fault(scrubKeys(detail));
          return null;
        }
        const jittered = Math.round(step * (1 + Math.random() * JITTER));
        await io.sleep(Math.min(LONGEST_WAIT_MS, Math.max(jittered, wait)));
      }
    },
  };
}
