import { scrubProcessStreams } from "@legion/contracts/held-secrets"
import { messageFor } from "@legion/envoy-client/errors"
import { sessionConfigFile, sessionEnvoySettings } from "@legion/envoy-client/session-settings"
import { EnvoySendUnconfirmedError } from "@legion/envoy-client/transport"
import { configuredSessionId } from "../src/claude-session"
import { sendEnvoyMessage } from "../src/envoy-client"
import { parseSendArguments, resendCommand, type SendArguments } from "../src/send-arguments"

/** The exit of a send that may or may not have arrived (sysexits' `EX_TEMPFAIL`), apart from a
 *  refusal's 1: a script tells the two apart by status, and resends only under the key the
 *  message names. */
const EXIT_UNCONFIRMED = 75

// Every line this command writes passes the scrub of the secrets it holds.
scrubProcessStreams()

let send: SendArguments | undefined
try {
  send = parseSendArguments(process.argv.slice(2))
  // The listener and bearer every command resolves: the envoy.json `envoyConfigPath("command")`
  // chooses (the ENVOY_CONFIG_FILE a session's plugin hands its shell first), with ENVOY_URL and
  // ENVOY_TOKEN_FILE or ENVOY_TOKEN over its envoy section. A send opens no NATS connection.
  // The session this command acts as, where its shell names one: a broker trade writes under it.
  const session = configuredSessionId({
    ENVOY_SESSION_ID: process.env["ENVOY_SESSION_ID"],
    CLAUDE_CODE_SESSION_ID: process.env["CLAUDE_CODE_SESSION_ID"],
  })
  const envoy = sessionEnvoySettings(sessionConfigFile("command", process.env), process.env, {
    cwd: process.cwd(),
    nats: false,
    ...(session === undefined ? {} : { session }),
  })
  if (!envoy.enabled) throw new Error(`Envoy is off: ${envoy.error ?? envoy.notice}`)
  await sendEnvoyMessage({
    baseUrl: envoy.url,
    credential: envoy.credential,
    targetSession: send.targetSession,
    message: send.message,
    ...(send.idempotencyKey === undefined ? {} : { idempotencyKey: send.idempotencyKey }),
  })
  process.stdout.write(`Sent Envoy message to ${send.targetSession}.\n`)
} catch (error) {
  if (error instanceof EnvoySendUnconfirmedError && send !== undefined) {
    // Not a failure: an attempt may have stored the message. The resend under its key is safe,
    // and a run without the key would deliver it twice.
    process.stderr.write(`Claude Envoy send not confirmed: ${messageFor(error)}\n`)
    if (error.resendSafe) {
      const script = process.argv[1] ?? "envoy-send.ts"
      process.stderr.write(
        `To send it again safely, run: ${resendCommand(script, error.idempotencyKey, send.targetSession, send.message)}\n`,
      )
    }
    process.exitCode = EXIT_UNCONFIRMED
  } else {
    process.stderr.write(`Claude Envoy send failed: ${messageFor(error)}\n`)
    process.exitCode = 1
  }
}
