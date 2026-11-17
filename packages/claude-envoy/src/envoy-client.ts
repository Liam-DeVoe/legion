import type { RequestCredential } from "@legion/envoy-client/bearer"
import { withoutUserinfo } from "@legion/envoy-client/errors"
import { redirectOrigin } from "@legion/envoy-client/redirect"
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
 * names (`@legion/envoy-client/redirect`): a 3xx fails the send, naming where it points.
 */
export async function sendEnvoyMessage(options: SendEnvoyMessageOptions): Promise<void> {
  const url = new URL("/v1/messages/send", options.baseUrl)
  const timeout = AbortSignal.timeout(SEND_TIMEOUT_MS)
  const deadline =
    options.signal === undefined ? timeout : AbortSignal.any([options.signal, timeout])
  const headers =
    (await options.credential?.headers("POST", url.toString(), undefined, deadline)) ?? {}
  const response = await ky.post(url, {
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
  if (response.status >= 300 && response.status <= 399) {
    const target = redirectOrigin(response, url.toString())
    throw new Error(
      `POST ${withoutUserinfo(url.toString())} answered ${response.status}${target === undefined ? "" : `, redirecting to ${target}`}, and a send follows no redirect: set the Envoy URL (ENVOY_URL, or envoy.json's envoy.url) to the listener's own address`,
    )
  }
}
