import assert from "node:assert/strict";
import { test } from "node:test";
import handler from "./functions/jev.mjs";

const requestBody = {
  model: "jev-latest",
  state: "18+🍆🍑!!",
  questions: {
    sexual_suggestiveness: {
      type: "noul", instructions: "Classify this message",
      criteria: { true: "Suggestive", false: "Ordinary" },
    },
  },
};

function setup(t) {
  for (const [key, value] of Object.entries({
    AI_PROVIDER_API_KEY: "bot-secret", TYPESAFE_API_KEY: "injected-gateway-key",
    TYPESAFE_BASE_URL: "https://gateway.example/typesafe/",
  })) {
    const original = process.env[key];
    process.env[key] = value;
    t.after(() => {
      if (original === undefined) delete process.env[key];
      else process.env[key] = original;
    });
  }
}

const request = (body = requestBody, token = "bot-secret") => new Request("https://example.netlify.app/api/jev", {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
  body: JSON.stringify(body),
});

test("relay uses injected credentials and returns only the score", async (t) => {
  setup(t);
  const fetch = t.mock.method(globalThis, "fetch", async (url, options) => {
    assert.equal(String(url), "https://gateway.example/typesafe/v1/systemone");
    assert.equal(options.headers.Authorization, "Bearer injected-gateway-key");
    assert.equal(options.redirect, "error");
    assert.deepEqual(JSON.parse(options.body), requestBody);
    return Response.json({ answers: { sexual_suggestiveness: { noul: 0.42 } }, state: "must not be returned" });
  });
  const response = await handler(request({ ...requestBody, providerOptions: { gateway: { only: ["other"] } } }));
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("Cache-Control"), "no-store");
  assert.deepEqual(await response.json(), { answers: { sexual_suggestiveness: { noul: 0.42 } } });
  assert.equal(fetch.mock.callCount(), 1);
});

test("authentication, invalid bodies, and missing config never call Gateway", async (t) => {
  setup(t);
  const fetch = t.mock.method(globalThis, "fetch", () => { throw new Error("Must not call Gateway"); });
  assert.equal((await handler(request(requestBody, "wrong"))).status, 401);
  assert.equal((await handler(new Request("https://example.netlify.app/api/jev"))).status, 405);
  for (const body of [null, {}, { ...requestBody, model: "other" }, { ...requestBody, state: " " },
    { ...requestBody, state: "あ".repeat(4001) }, { ...requestBody, questions: {} }]) {
    assert.equal((await handler(request(body))).status, 400);
  }
  assert.equal((await handler(request({ ...requestBody, state: "a".repeat(40000) }))).status, 413);
  assert.equal((await handler(new Request("https://example.netlify.app/api/jev", {
    method: "POST", headers: { "Content-Type": "application/json", Authorization: "Bearer bot-secret" }, body: "{",
  }))).status, 400);
  delete process.env.TYPESAFE_API_KEY;
  assert.equal((await handler(request())).status, 503);
  delete process.env.AI_PROVIDER_API_KEY;
  assert.equal((await handler(request())).status, 503);
  assert.equal(fetch.mock.callCount(), 0);
});

test("upstream failures and invalid scores are rejected without exposing bodies", async (t) => {
  setup(t);
  for (const status of [401, 402, 429, 500]) {
    t.mock.method(globalThis, "fetch", async () => new Response("sensitive submitted text", { status }));
    const response = await handler(request());
    assert.equal(response.status, status === 429 ? 429 : 502);
    assert.equal((await response.text()).includes("sensitive"), false);
    t.mock.restoreAll();
  }
  for (const score of [null, "0.42", -0.01, 1.01]) {
    t.mock.method(globalThis, "fetch", async () => Response.json({ answers: { sexual_suggestiveness: { noul: score } } }));
    assert.equal((await handler(request())).status, 502);
    t.mock.restoreAll();
  }
  t.mock.method(globalThis, "fetch", async () => { throw new DOMException("secret", "TimeoutError"); });
  assert.equal((await handler(request())).status, 504);
});
