/**
 * A preload for a child `bun test` run (`bun test --preload ./test/nats-default-port-held.ts …`):
 * in that process alone, nats.js's default 127.0.0.1:4222 reads as held by another program, and
 * no program binds that number. A bind of port 4222 (`Bun.listen`, or the `listen` of a
 * `node:net` server) fails with EADDRINUSE, as it does beside a holder, and a TCP dial of port
 * 4222 through `node:net` reaches the rival at NATS_DEFAULT_PORT_RIVAL instead: a listener on a
 * port the kernel chose, which the parent test holds. So the child sees a machine whose 4222
 * another program holds, and two such runs, or a local NATS on the real 4222, share nothing.
 * `Bun.serve` and `Bun.connect` are not covered: no row that runs under it uses them.
 */
import net from "node:net";

const NATS_DEFAULT_PORT = 4222;
const rival = Number(process.env.NATS_DEFAULT_PORT_RIVAL);
if (!Number.isInteger(rival) || rival <= 0 || rival > 65_535) {
  throw new Error(
    `NATS_DEFAULT_PORT_RIVAL is "${process.env.NATS_DEFAULT_PORT_RIVAL ?? ""}", not the port of the rival listener`
  );
}

function namesDefaultPort(port: unknown): boolean {
  return (
    (typeof port === "number" || typeof port === "string") && Number(port) === NATS_DEFAULT_PORT
  );
}

function addressInUse(): Error {
  return Object.assign(
    new Error(
      `listen EADDRINUSE: address already in use 127.0.0.1:${NATS_DEFAULT_PORT} (held by the rival on ${rival})`
    ),
    { code: "EADDRINUSE", syscall: "listen", port: NATS_DEFAULT_PORT }
  );
}

const { listen } = Bun;
Object.assign(Bun, {
  listen(...args: unknown[]): unknown {
    const [options] = args;
    if (
      typeof options === "object" &&
      options !== null &&
      "port" in options &&
      namesDefaultPort(options.port)
    ) {
      throw addressInUse();
    }
    return Reflect.apply(listen, Bun, args);
  },
});

const serverListen = net.Server.prototype.listen;
Object.assign(net.Server.prototype, {
  listen(this: net.Server, ...args: unknown[]): net.Server {
    const [first] = args;
    const port =
      typeof first === "object" && first !== null && "port" in first ? first.port : first;
    if (!namesDefaultPort(port)) return Reflect.apply(serverListen, this, args);
    process.nextTick(() => this.emit("error", addressInUse()));
    return this;
  },
});

/** The dial's arguments with a dial of port 4222 sent to the rival; every other dial unchanged. */
function toRival(args: unknown[]): unknown[] {
  const [first, ...rest] = args;
  if (
    typeof first === "object" &&
    first !== null &&
    "port" in first &&
    namesDefaultPort(first.port)
  ) {
    return [{ ...first, host: "127.0.0.1", port: rival }, ...rest];
  }
  if (namesDefaultPort(first)) {
    return [rival, "127.0.0.1", ...rest.filter((arg) => typeof arg === "function")];
  }
  return args;
}

const { createConnection } = net;
const dial = (...args: unknown[]): net.Socket =>
  Reflect.apply(createConnection, net, toRival(args));
Object.assign(net, { createConnection: dial, connect: dial });
