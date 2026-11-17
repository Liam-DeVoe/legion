import { describe, expect, test } from "bun:test";
import * as settingUrl from "./setting-url";
import { shownSettingUrl } from "./setting-url";

// One function decides what a line may show of a setting's URL. A second export that strips a
// user part without that judgement is how a value the parser misread (`user:password@host`) was
// printed whole, so the module exports the one function and nothing else.
test("the module exports shownSettingUrl alone", () => {
  expect(Object.keys(settingUrl)).toEqual(["shownSettingUrl"]);
});

// A setting's value is shown only when the URL parser reads it as an http or https URL with a
// host, and then with no user and no password. Any other reading cannot be trusted to have found
// the password (`user:password@host` parses, with `user:` as its scheme and the password in its
// path), so such a value is shown not at all: a line names its length alone.
describe("shownSettingUrl", () => {
  const user = "user-7f2a";
  const secret = "pw-4c7e19";

  // A configured Dispatch or Envoy URL may carry a user and a password, and a user alone can be a
  // token, so a line that names the URL drops both and keeps the rest as it was written.
  test.each([
    [`https://${user}:${secret}@host.example/a`, "https://host.example/a"],
    [`HTTPS://${user}:${secret}@Host.Example/a`, "https://host.example/a"],
    [`http://${user}@127.0.0.1:8766`, "http://127.0.0.1:8766"],
    ["http://dispatch.test", "http://dispatch.test"],
    [`https://${user}:${secret}@host.example`, "https://host.example"],
    [`https://${user}:${secret}@host.example/`, "https://host.example/"],
    [
      `http://${user}:${secret}@host.example:8766/api/v1/issues?project=DSP`,
      "http://host.example:8766/api/v1/issues?project=DSP",
    ],
    [`https://${user}@host.example/dispatch`, "https://host.example/dispatch"],
    [`https://:${secret}@host.example`, "https://host.example"],
  ])("%p is shown as %p", (value, shown) => {
    expect(shownSettingUrl(value)).toBe(shown);
  });

  test("a URL with no user or password comes back as written", () => {
    for (const url of ["http://dispatch.test", "https://host.example/a@b", "HTTP://Host.Example"]) {
      expect(shownSettingUrl(url)).toBe(url);
    }
  });

  test.each([
    [`${user}:${secret}@host.example/a`],
    [`${user}:${secret}@host.example:8443`],
    [`mailto:${user}:${secret}@host.example`],
    [`//${user}:${secret}@host.example/a`],
    ["   "],
    [""],
    ["not a url"],
    [`ftp://${user}:${secret}@host.example/a`],
    [`file://${user}:${secret}@host.example/a`],
    [`https://${user}:${secret}@bad host/a`],
    [`https://${user}:${secret}/x@host.example`],
    [`https://${user}:${secret}@host.example:99999`],
  ])("%p is shown not at all", (value) => {
    expect(shownSettingUrl(value)).toBeUndefined();
  });
});
