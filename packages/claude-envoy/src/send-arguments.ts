import { z } from "zod"

const SendArgumentsSchema = z.tuple([z.string().min(1), z.string().min(1)]).rest(z.string().min(1))

/** The flag that sends a message under a key the caller names: the one an unconfirmed send
 *  printed, so the listener drops the resend as a repeat if the first copy arrived. */
export const IDEMPOTENCY_KEY_FLAG = "--idempotency-key"

export type SendArguments = {
  readonly targetSession: string
  readonly message: string
  /** The key the send goes under, where the caller names one; one is minted otherwise. */
  readonly idempotencyKey?: string
}

/**
 * `[--idempotency-key <key>] <target session> <message words…>`. The flag comes first, so a
 * message whose words begin with it is still a message.
 */
export function parseSendArguments(arguments_: readonly string[]): SendArguments {
  let rest = arguments_
  let idempotencyKey: string | undefined
  if (rest[0] === IDEMPOTENCY_KEY_FLAG) {
    idempotencyKey = z.string().min(1).parse(rest[1])
    rest = rest.slice(2)
  }
  const [targetSession, ...words] = SendArgumentsSchema.parse(rest)
  return {
    targetSession,
    message: words.join(" "),
    ...(idempotencyKey === undefined ? {} : { idempotencyKey }),
  }
}

/** `value` as one word of a POSIX shell command: single-quoted, with each `'` closed, escaped
 *  and reopened. */
function shellWord(value: string): string {
  return /^[A-Za-z0-9_./:=@%+-]+$/.test(value) ? value : `'${value.replaceAll("'", "'\\''")}'`
}

/**
 * The command that sends the same message again under `idempotencyKey`, as `envoy-send` was run
 * (`bun <script>`): the resend an unconfirmed send names, which the listener drops as a repeat
 * if the first copy arrived.
 */
export function resendCommand(
  script: string,
  idempotencyKey: string,
  targetSession: string,
  message: string,
): string {
  return ["bun", script, IDEMPOTENCY_KEY_FLAG, idempotencyKey, targetSession, message]
    .map(shellWord)
    .join(" ")
}
