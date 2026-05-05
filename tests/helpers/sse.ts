/**
 * Minimal SSE consumer. Yields parsed JSON frames as they arrive.
 *
 * Frame format on wire: `data: <json>\n\n` (or `data: [DONE]\n\n` to terminate).
 * Comments (`: keep-alive\n\n`) are skipped.
 */

export type SseFrame = {
  type: string;
  data?: unknown;
  errorText?: string;
  // Some events (error) carry fields at the top level.
  [key: string]: unknown;
};

export async function* consumeSse(
  res: Response,
): AsyncGenerator<SseFrame, void, void> {
  if (!res.body) throw new Error('SSE response has no body');
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let sep: number;
    while ((sep = buffer.indexOf('\n\n')) !== -1) {
      const raw = buffer.slice(0, sep);
      buffer = buffer.slice(sep + 2);
      if (!raw) continue;
      // Strip "data: " prefix; ignore comments and other lines.
      const dataLine = raw
        .split('\n')
        .find((line) => line.startsWith('data: '));
      if (!dataLine) continue;
      const payload = dataLine.slice('data: '.length);
      if (payload === '[DONE]') return;
      try {
        yield JSON.parse(payload) as SseFrame;
      } catch (err) {
        throw new Error(
          `failed to parse SSE frame: ${err instanceof Error ? err.message : err}; payload=${payload.slice(0, 200)}`,
        );
      }
    }
  }
}

export async function collectFrames(res: Response): Promise<SseFrame[]> {
  const frames: SseFrame[] = [];
  for await (const frame of consumeSse(res)) {
    frames.push(frame);
  }
  return frames;
}
