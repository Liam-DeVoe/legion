import type { RequestCredential } from "@legion/envoy-client/bearer"
import { createEnvoyClient } from "@legion/envoy-client/transport"

export type SendEnvoyMessageOptions = {
  readonly baseUrl: string
  /** What the send presents (`EnabledEnvoySettings.credential`): the listener's bearer, this
   *  machine's broker credential, which signs a DPoP proof for this request, or none, which
   *  sends no `Authorization` header, for a loopback listener that asks for none. */
  readonly credential: RequestCredential | undefined
  readonly targetSession: string
  readonly message: string
}

/**
 * Sends one direct message through envoy-client's own send (`createEnvoyClient().send`), so the
 * `envoy-send` command keeps every rule a session's own send keeps:
 * - one idempotency key for the message, on every attempt, so the listener stores it at most
 *   once however many attempts reach it;
 * - an attempt that got no answer or a 5xx is sent again under that key, after 250 ms;
 * - a send that may have arrived is told so (`EnvoySendUnconfirmedError`), naming the key that
 *   resends it safely, never "failed";
 * - each attempt's credential waits the attempt less the request's reserve
 *   (`credentialDeadline`), and a credential that could not be minted lets the request go
 *   without it, which a listener that asks for no credential answers. A listener that wants one
 *   answers 401 at its auth gate, and the error names both halves and no resend, since nothing
 *   was stored.
 */
export async function sendEnvoyMessage(options: SendEnvoyMessageOptions): Promise<void> {
  const client = createEnvoyClient({
    baseUrl: options.baseUrl,
    fetch: globalThis.fetch,
    credential: options.credential,
  })
  // No source session: the body names none, as `envoy-send` never has, and the listener stamps
  // the message `agent` from the credential it was sent with.
  await client.send({
    sourceSessionID: "",
    targetSessionID: options.targetSession,
    message: options.message,
  })
}
