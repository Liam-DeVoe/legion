// packages/broker-client/scripts/print-trade.ts
// One trade, as a Dispatch or Envoy client makes it, for scripts/project-secrets-check.sh: it builds
// the client brokerTokenClient builds from this process's environment (AGENT_SECRETS_URL, the
// machine identity, a person's sign-in on this machine, AGENT_SECRETS_SESSION and
// AGENT_SECRETS_PROJECTS), trades once, and prints the access token on standard output alone. A
// refusal prints the broker's own words on standard error and exits 3; an environment that makes
// no client exits 2. It prints nothing else, so a caller reads the token with $(...).
import { brokerTokenClient } from "../src/environment.ts";

const made = await brokerTokenClient(process.env);
if (made?.client === undefined) {
  process.stderr.write(
    "print-trade: this environment makes no broker client: set AGENT_SECRETS_URL and AGENT_SECRETS_MACHINE_IDENTITY\n"
  );
  process.exit(2);
}
try {
  const token = await made.client.token();
  process.stdout.write(`${token.accessToken}\n`);
} catch (error) {
  process.stderr.write(`print-trade: ${error instanceof Error ? error.message : String(error)}\n`);
  process.exit(3);
}
