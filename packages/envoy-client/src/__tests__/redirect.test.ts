import { expect, test } from "bun:test";
import { redirectOrigin } from "../redirect";

const redirect = (status: number, location?: string) =>
  new Response(null, { status, headers: location === undefined ? {} : { Location: location } });

// The origin tells a person who wrote `http://` for an `https://` server what to correct: the
// scheme and the host. Never a path, a query, a fragment or a user, any of which can carry a secret.
test("a redirect names the origin its Location points at, and nothing else of it", () => {
  // A target that carries a user and a password, built from its parts; the password is made for
  // this run, so no line of the test holds a credential.
  const hidden = crypto.randomUUID();
  const withUser = new URL("https://dispatch.example.com/api/v1/issues/ACME-1");
  withUser.username = "user";
  withUser.password = hidden;
  for (const [location, origin] of [
    [
      "https://sso.example.com:8443/start?session=s3cr3t-query#frag",
      "https://sso.example.com:8443",
    ],
    ["https://dispatch.example.com:443/api", "https://dispatch.example.com"],
    [withUser.href, "https://dispatch.example.com"],
    ["/login?next=%2Fapi", "http://dispatch.test"],
  ] as const) {
    for (const status of [301, 302, 303, 307, 308]) {
      const named = redirectOrigin(redirect(status, location), "http://dispatch.test/api/v1/x");
      expect(named, location).toBe(origin);
      for (const part of ["/start", "s3cr3t-query", "frag", hidden, "user", "next="]) {
        expect(named, location).not.toContain(part);
      }
    }
  }
});

test("a Location that is missing, unreadable or not http(s) names no origin", () => {
  for (const response of [
    redirect(307),
    redirect(302, "http://[not a host"),
    redirect(301, "ftp://files.example.com/x"),
    redirect(300, "javascript:alert(1)"),
  ]) {
    expect(redirectOrigin(response, "http://dispatch.test/")).toBeUndefined();
  }
});

test("an answer that is no 3xx names no origin, whatever its Location", () => {
  for (const status of [200, 204, 299, 400, 404, 500]) {
    expect(
      redirectOrigin(redirect(status, "https://elsewhere.example"), "http://dispatch.test/")
    ).toBeUndefined();
  }
});
