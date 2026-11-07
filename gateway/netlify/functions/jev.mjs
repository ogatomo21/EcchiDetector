import { timingSafeEqual } from "node:crypto";

const json = (body, status = 200) => Response.json(body, {
  status,
  headers: { "Cache-Control": "no-store" },
});
const error = (message, status) => json({ error: { message } }, status);

async function readJSON(body, limit) {
  const reader = body?.getReader();
  if (!reader) return undefined;
  const chunks = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > limit) throw new RangeError("Body too large");
      chunks.push(Buffer.from(value));
    }
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } finally {
    await reader.cancel();
    reader.releaseLock();
  }
}

export default async function handler(req) {
  if (req.method !== "POST") return error("POST required", 405);

  const secret = process.env.AI_PROVIDER_API_KEY?.trim();
  if (!secret) return error("Relay authentication is not configured", 503);
  const supplied = Buffer.from(req.headers.get("Authorization") ?? "");
  const expected = Buffer.from(`Bearer ${secret}`);
  if (supplied.length !== expected.length || !timingSafeEqual(supplied, expected)) {
    return error("Unauthorized", 401);
  }
  if (req.headers.get("Content-Type")?.split(";")[0].trim().toLowerCase() !== "application/json") {
    return error("application/json required", 415);
  }

  let body;
  try {
    body = await readJSON(req.body, 32768);
  } catch (err) {
    return error(err instanceof RangeError ? "Request too large" : "Invalid JSON", err instanceof RangeError ? 413 : 400);
  }
  const question = body?.questions?.sexual_suggestiveness;
  if (body?.model !== "jev-latest" || typeof body?.state !== "string" || !body.state.trim() ||
      Array.from(body.state).length > 4000 || Object.keys(body.questions ?? {}).length !== 1 ||
      question?.type !== "noul" || typeof question.instructions !== "string" ||
      !question.instructions.trim() || question.instructions.length > 2000 ||
      typeof question.criteria?.true !== "string" || typeof question.criteria?.false !== "string" ||
      question.criteria.true.length > 2000 || question.criteria.false.length > 2000) {
    return error("Invalid EcchiDetector classification request", 400);
  }

  let endpoint;
  const apiKey = process.env.TYPESAFE_API_KEY?.trim();
  try {
    endpoint = new URL(process.env.TYPESAFE_BASE_URL);
    if (!apiKey || endpoint.protocol !== "https:" || endpoint.username || endpoint.password || endpoint.search || endpoint.hash) {
      throw new Error("Invalid Gateway configuration");
    }
    endpoint.pathname = endpoint.pathname.replace(/\/$/, "") + "/v1/systemone";
  } catch {
    return error("Netlify AI Gateway is not configured", 503);
  }

  try {
    // Keep the prompt in Go; forward only the supported classification fields.
    const upstream = await fetch(endpoint, {
      method: "POST",
      redirect: "error",
      signal: AbortSignal.timeout(15000),
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey}` },
      body: JSON.stringify({
        model: "jev-latest",
        state: body.state,
        questions: {
          sexual_suggestiveness: {
            type: "noul",
            instructions: question.instructions,
            criteria: { true: question.criteria.true, false: question.criteria.false },
          },
        },
      }),
    });
    if (!upstream.ok) {
      await upstream.body?.cancel();
      // Upstream error bodies can include submitted text or credentials.
      return error(`Netlify AI Gateway returned HTTP ${upstream.status}`, upstream.status === 429 ? 429 : 502);
    }
    const result = await readJSON(upstream.body, 65536);
    const score = result?.answers?.sexual_suggestiveness?.noul;
    if (typeof score !== "number" || !Number.isFinite(score) || score < 0 || score > 1) {
      return error("Invalid Jev response", 502);
    }
    return json({ answers: { sexual_suggestiveness: { noul: score } } });
  } catch (err) {
    return error(err?.name === "TimeoutError" ? "AI Gateway timed out" : "AI Gateway request failed", err?.name === "TimeoutError" ? 504 : 502);
  }
}

export const config = { path: "/api/jev", method: "POST" };
