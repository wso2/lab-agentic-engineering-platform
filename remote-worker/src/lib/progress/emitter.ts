// Single-owner stdout writer for runner progress NDJSON.
// All progress events flow through emit(); nothing else writes to stdout
// in the runner code path. Stamps ts + seq so callers can't forget,
// and routes every line through the scrubber.

import { scrubber } from "./scrubber.js";
import { PROGRESS_SCHEMA_VERSION, type ProgressEvent, type ProgressEventInput } from "./schema.js";

let seqCounter = 0;

export function emit(event: ProgressEventInput): void {
  seqCounter += 1;
  const enriched = {
    schemaVersion: PROGRESS_SCHEMA_VERSION,
    ts: new Date().toISOString(),
    seq: seqCounter,
    ...event,
  } as ProgressEvent;
  const line = scrubber.scrub(JSON.stringify(enriched));
  process.stdout.write(line + "\n");
}

export function primeScrubber(secrets: Iterable<string | undefined | null>): void {
  for (const s of secrets) scrubber.addLiteral(s ?? undefined);
}

// Test seam.
export function _resetEmitterForTesting(): void {
  seqCounter = 0;
  scrubber.reset();
}
