// The generated Dispatch frames of #5192's property, drawn as the edge listener's side draws them
// (generatedDispatchFrames in packages/envoy/cmd/listener/edge_dispatch_property_test.go): Go's
// math/rand/v2 PCG seeded (5192, 11), its IntN and Shuffle, and the same pools in the same order.
// The two sides must write the same bytes, which dispatchFramesDigest and the fixture's sha256
// hold; a change to either generator alone fails the side it was made on.
import { createHash } from "node:crypto";

const MASK64 = (1n << 64n) - 1n;
const MASK128 = (1n << 128n) - 1n;
const MUL = (2549297995355413924n << 64n) | 4865540595714422341n;
const INC = (6364136223846793005n << 64n) | 1442695040888963407n;
const CHEAP_MUL = 0xda942042e4dd58b5n;

/** Go's math/rand/v2 PCG with the Rand methods the generator calls. */
export class GoPCG {
  private state: bigint;

  constructor(seed1: bigint, seed2: bigint) {
    this.state = (seed1 << 64n) | seed2;
  }

  /** PCG.Uint64: the 128-bit LCG step, then DXSM on its high half. */
  uint64(): bigint {
    this.state = (this.state * MUL + INC) & MASK128;
    let hi = this.state >> 64n;
    const lo = this.state & MASK64;
    hi ^= hi >> 32n;
    hi = (hi * CHEAP_MUL) & MASK64;
    hi ^= hi >> 48n;
    return (hi * (lo | 1n)) & MASK64;
  }

  /** Rand.uint64n on a 64-bit build (Lemire's reduction with Go's rejection rule). */
  uint64n(n: bigint): bigint {
    if ((n & (n - 1n)) === 0n) return this.uint64() & (n - 1n);
    let product = this.uint64() * n;
    if ((product & MASK64) < n) {
      const thresh = ((1n << 64n) - n) % n;
      while ((product & MASK64) < thresh) product = this.uint64() * n;
    }
    return product >> 64n;
  }

  intN(n: number): number {
    return Number(this.uint64n(BigInt(n)));
  }

  shuffle<T>(list: T[]): void {
    for (let i = list.length - 1; i > 0; i -= 1) {
      const j = Number(this.uint64n(BigInt(i + 1)));
      [list[i], list[j]] = [list[j] as T, list[i] as T];
    }
  }
}

/** How many frames generatedDispatchFrames writes. */
export const DISPATCH_PROPERTY_FRAMES = 100_000;

/** The 100,000 generated frames, in the edge's side's order. */
export function generatedDispatchFrames(): string[] {
  const r = new GoPCG(5192n, 11n);
  const pick = (pool: readonly string[]): string => pool[r.intN(pool.length)] as string;
  const own = "notifications.agent.ses_v";
  const projects = [
    `"PRIV"`,
    `"PUB"`,
    `""`,
    `"priv"`,
    `" PRIV"`,
    String.raw`"P\u0052IV"`,
    String.raw`"P\u0055B"`,
    "null",
    "5",
  ];
  const issues = [
    `"PRIV-1"`,
    `"PUB-1"`,
    `"priv-1"`,
    `" PUB-1"`,
    `"PUB-01"`,
    `"PUB-1 "`,
    String.raw`"P\u0055B-1"`,
    String.raw`"P\u0052IV-1"`,
    "null",
    `"PUB"`,
    `"PRIV-x"`,
    `"PRIV-1-2"`,
  ];
  const frames: string[] = [];
  for (let n = 0; n < DISPATCH_PROPERTY_FRAMES; n += 1) {
    const fields: string[] = [];
    const add = (key: string, value: string) => fields.push(`"${key}":${value}`);
    switch (r.intN(5)) {
      case 0:
        break;
      case 1:
      case 2:
        add(
          pick(["project", "project", "project", "Project", String.raw`pr\u006fject`]),
          pick(projects)
        );
        break;
      default:
        add("project", pick(projects));
        if (r.intN(4) === 0) add(pick(["project", "PROJECT"]), pick(projects));
    }
    switch (r.intN(5)) {
      case 0:
        break;
      case 1:
      case 2:
        add(
          pick(["issue_key", "issue_key", "issue_key", "Issue_Key", String.raw`issue_k\u0065y`]),
          pick(issues)
        );
        break;
      default:
        add("issue_key", pick(issues));
        if (r.intN(4) === 0) add(pick(["issue_key", "ISSUE_KEY"]), pick(issues));
    }
    add("type", `"comment.created"`);
    add("actor", `{"kind":"person","id":"owner@example.com"}`);
    add("notify", "true");
    add("payload", `{"id":"c-1","body":"BODY"}`);
    if (r.intN(12) === 0) add("__proto__", `{"project":"PUB"}`);
    r.shuffle(fields);
    const event = `{${fields.join(",")}}`;
    let payload: string;
    switch (r.intN(6)) {
      case 0:
      case 1:
        payload = event;
        break;
      case 2:
      case 3:
        payload = `{"event":${event},"delivery":{"attempt":1,"mode":"steer"}}`;
        break;
      case 4:
        payload = `{"delivery":{"attempt":1},"event":${event},"project":${pick(projects)}}`;
        break;
      default:
        payload = `{"event":${event},"project":${pick(projects)},"issue_key":${pick(issues)}}`;
    }
    const source = pick([`"dispatch"`, `"dispatch"`, `"dispatch"`, String.raw`"d\u0069spatch"`]);
    const topic = pick([
      own,
      own,
      "notifications.role.legion-priv-priv-1-tester",
      "notifications.dispatch.issue.PRIV.PRIV-1.comment.created",
    ]);
    frames.push(
      `{"source":${source},"event_id":"g","topic":"${topic}","payload":${JSON.stringify(payload)}}`
    );
  }
  return frames;
}

/** The sha256 of frames, each followed by a line feed (dispatchFramesDigest on the Go side). */
export function dispatchFramesDigest(frames: readonly string[]): string {
  const digest = createHash("sha256");
  for (const frame of frames) digest.update(`${frame}\n`);
  return digest.digest("hex");
}
