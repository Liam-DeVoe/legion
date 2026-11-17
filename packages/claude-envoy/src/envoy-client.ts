import type { RequestCredential } from "@legion/envoy-client/bearer"
import { withoutUserinfo } from "@legion/envoy-client/errors"
import { OWN_PATH_CHECK, redirectedTo, redirectTarget } from "@legion/envoy-client/redirect"
import ky from "ky"

/** How long one send may take, the credential's trade included. */
const SEND_TIMEOUT_MS = 5_000

export type SendEnvoyMessageOptions = {
  readonly baseUrl: string
  /** What the send presents (`EnabledEnvoySettings.credential`): the listener's bearer, this
   *  machine's broker credential, which signs a DPoP proof for this request, or none, which
   *  sends no `Authorization` header, for a loopback listener that asks for none. */
  readonly credential: RequestCredential | undefined
  readonly targetSession: string
  readonly message: string
  /** Ends the send early, beside its own {@link SEND_TIMEOUT_MS}. */
  readonly signal?: AbortSignal | undefined
}

/**
 * Sends one direct message. One deadline bounds the whole send, and the credential is given it
 * too (`RequestCredential.headers`): a broker credential waiting on a pod's missing registration
 * stops at it, naming the session, while its trade goes on in the background. A send follows no
 * redirect, since fetch would send the broker's DPoP proof and the message on to the origin it
 * names (`@legion/envoy-client/redirect`): a 3xx fails the send, naming where it points. The
 * Envoy URL may carry a user and a password, which the send keeps; each line it fails with names
 * the URL with neither (`withoutUserinfo`).
 */
export async function sendEnvoyMessage(options: SendEnvoyMessageOptions): Promise<void> {
  const url = new URL("/v1/messages/send", options.baseUrl)
  const shown = withoutUserinfo(url.href)
  const timeout = AbortSignal.timeout(SEND_TIMEOUT_MS)
  const deadline =
    options.signal === undefined ? timeout : AbortSignal.any([options.signal, timeout])
  const headers =
    (await options.credential?.headers("POST", url.toString(), undefined, deadline)) ?? {}
  let response: Response
  try {
    response = await ky.post(url, {
      json: {
        target_session: options.targetSession,
        message: options.message,
      },
      headers,
      retry: 0,
      timeout: false,
      signal: deadline,
      redirect: "manual",
      // A 3xx is refused below, with where it points; every other status that is no success is
      // ky's HTTPError, as before.
      throwHttpErrors: (status) => status < 300 || status > 399,
    })
  } catch (error) {
    // ky's HTTPError names the request by its URL, user and password included: such an error
    // becomes one of the send's own, the URL cleaned, with it as the cause. Every other error, a
    // timeout's or an abort's `DOMException` among them, goes on as it was thrown.
    if (!(error instanceof Error)) throw error
    const cleaned = error.message.replaceAll(url.href, shown)
    if (cleaned === error.message) throw error
    throw new Error(cleaned, { cause: error })
  }
  const target = redirectTarget(response, url.href)
  if (target !== undefined) {
    const fix =
      target.kind === "the same origin"
        ? OWN_PATH_CHECK
        : "set the Envoy URL (ENVOY_URL, or envoy.json's envoy.url) to the listener's own address"
    throw new Error(
      `POST ${shown} answered ${response.status}, ${redirectedTo(target)}, and a send follows no redirect: ${fix}`,
    )
  }
}
