import { credentialDeadline, type RequestCredential } from "@legion/envoy-client/bearer"
import { messageFor } from "@legion/envoy-client/errors"
import ky, { HTTPError } from "ky"

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
 * Sends one direct message, as envoy-client's transport sends a call (`attemptOnce`). One deadline
 * bounds the whole send, and the credential waits on all of it but the request's reserve
 * (`credentialDeadline`). A credential that could not mint in that time — a broker credential
 * waiting on a pod's missing registration, or on a broker that does not answer — leaves the send
 * to go without it in the reserve, which a listener that asks for no credential answers; one that
 * wants a credential answers 401, and the error names both halves. The trade goes on in the
 * background either way.
 */
export async function sendEnvoyMessage(options: SendEnvoyMessageOptions): Promise<void> {
  const url = new URL("/v1/messages/send", options.baseUrl)
  const timeout = AbortSignal.timeout(SEND_TIMEOUT_MS)
  const deadline =
    options.signal === undefined ? timeout : AbortSignal.any([options.signal, timeout])
  let headers: Readonly<Record<string, string>> = {}
  let unminted: unknown
  try {
    headers =
      (await options.credential?.headers(
        "POST",
        url.toString(),
        undefined,
        credentialDeadline(deadline, SEND_TIMEOUT_MS),
      )) ?? {}
  } catch (error) {
    // The send's own deadline leaves nothing to send: the credential's words are the send's.
    if (deadline.aborted) throw error
    unminted = error
  }
  try {
    await ky.post(url, {
      json: {
        target_session: options.targetSession,
        message: options.message,
      },
      headers,
      retry: 0,
      timeout: false,
      signal: deadline,
    })
  } catch (error) {
    if (unminted !== undefined && error instanceof HTTPError && error.response.status === 401) {
      throw new Error(
        `the send answered 401, and this machine's Envoy credential could not be minted: ${messageFor(unminted)}`,
        { cause: unminted },
      )
    }
    throw error
  }
}
