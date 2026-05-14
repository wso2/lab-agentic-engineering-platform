import type express from "express";
import { z } from "zod";
import { streamText } from "ai";
import { createAnthropic } from "@ai-sdk/anthropic";
import { config } from "../../shared/config.js";
import { getDocumentGenerationSkill } from "../../skills/document-generation/index.js";
import {
  resolveAnthropicKey,
  AnthropicKeyError,
} from "../../shared/anthropic-key-resolver.js";
import { getOrgId } from "../../middleware/org-id.js";

const RequestBody = z.object({
  sources: z.record(z.string(), z.string()).optional(),
  prompt: z.string().optional(),
});

/**
 * Generic document-generation route. Path: `/v1/agents/document-generation/{skillId}`.
 *
 * The skillId selects a registered DocumentGenerationSkill (see
 * skills/document-generation/index.ts). The body carries optional source
 * files (filename → content) and an optional user prompt. Response is the
 * AI SDK UI Message Stream over SSE.
 *
 * Skills that declare a `postProcess` hook (e.g. wireframes / domain-model
 * which emit a tiny DSL but persist as Excalidraw JSON) get a final
 * `text-delta` chunk with `{ replace: true, delta: <transformed> }` emitted
 * after the live stream finishes. The BFF treats `replace: true` as a
 * signal to discard everything previously accumulated and use only this
 * payload as the file content.
 */
export function registerDocumentGeneration(app: express.Express) {
  app.post(
    "/v1/agents/document-generation/:skillId",
    async (req, res) => {
      const skillId = req.params.skillId;
      const skill = getDocumentGenerationSkill(skillId);
      if (!skill) {
        res.status(404).json({ error: `unknown skill: ${skillId}` });
        return;
      }

      const parsed = RequestBody.safeParse(req.body);
      if (!parsed.success) {
        res.status(400).json({ error: parsed.error.format() });
        return;
      }

      const userPrompt = skill.buildUserPrompt({
        sources: parsed.data.sources ?? {},
        prompt: parsed.data.prompt,
      });

      const orgId = getOrgId(res);
      let anthropicApiKey: string;
      try {
        anthropicApiKey = (await resolveAnthropicKey(orgId)).key;
      } catch (err) {
        if (err instanceof AnthropicKeyError) {
          res.status(err.status).json({ error: err.message, code: err.code });
          return;
        }
        throw err;
      }
      const anthropic = createAnthropic({ apiKey: anthropicApiKey });

      console.log(
        `[document-generation/${skillId}] streaming orgId=${orgId} (user-prompt ${userPrompt.length} chars, sources=${Object.keys(parsed.data.sources ?? {}).length})`,
      );

      res.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache, no-transform",
        Connection: "keep-alive",
        "x-vercel-ai-ui-message-stream": "v1",
      });
      res.flushHeaders?.();

      const abortController = new AbortController();
      res.on("close", () => {
        if (!res.writableEnded) abortController.abort();
      });

      const result = streamText({
        model: anthropic(config.model),
        system: skill.systemPrompt,
        prompt: userPrompt,
        abortSignal: abortController.signal,
        onError: ({ error }) => {
          console.error(`[document-generation/${skillId}] streamText error:`, error);
        },
        onFinish: (ev) => {
          console.log(
            `[document-generation/${skillId}] finish=${ev.finishReason} in=${ev.usage.inputTokens ?? 0} out=${ev.usage.outputTokens ?? 0}`,
          );
        },
      });

      // Buffer the live stream so we can apply the skill's post-processor on
      // close. The downstream consumer still sees every chunk in real time
      // (used by the console for best-effort live preview).
      let accumulated = "";
      let sawFinish = false;

      try {
        for await (const chunk of result.toUIMessageStream()) {
          if (abortController.signal.aborted) break;
          if (
            typeof chunk === "object" &&
            chunk !== null &&
            (chunk as { type?: string }).type === "text-delta"
          ) {
            const delta = (chunk as { delta?: unknown }).delta;
            if (typeof delta === "string") accumulated += delta;
          }
          if (
            typeof chunk === "object" &&
            chunk !== null &&
            (chunk as { type?: string }).type === "finish"
          ) {
            sawFinish = true;
          }
          res.write(`data: ${JSON.stringify(chunk)}\n\n`);
        }

        if (skill.postProcess && sawFinish && !abortController.signal.aborted) {
          try {
            const transformed = skill.postProcess.transform(accumulated);
            // Emit a synthetic text-delta with replace:true. Keeps wire format
            // additive — older clients (BFF without replace handling) will
            // append it; newer clients reset their buffer.
            const replaceChunk = {
              type: "text-delta",
              delta: transformed,
              replace: true,
            };
            res.write(`data: ${JSON.stringify(replaceChunk)}\n\n`);
            console.log(
              `[document-generation/${skillId}] post-processed ${accumulated.length} -> ${transformed.length} chars`,
            );
          } catch (err) {
            console.error(`[document-generation/${skillId}] postProcess failed:`, err);
            const errorChunk = {
              type: "error",
              errorText: err instanceof Error ? err.message : String(err),
            };
            res.write(`data: ${JSON.stringify(errorChunk)}\n\n`);
          }
        }

        res.write("data: [DONE]\n\n");
      } catch (err) {
        console.error(`[document-generation/${skillId}] pipe error:`, err);
        const errorChunk = {
          type: "error",
          errorText: err instanceof Error ? err.message : String(err),
        };
        res.write(`data: ${JSON.stringify(errorChunk)}\n\n`);
      } finally {
        res.end();
      }
    },
  );
}
