import fs from "node:fs";
import path from "node:path";

export interface TaskLog {
  write(data: unknown): void;
  close(): void;
}

export function openTaskLog(workspacePath: string): TaskLog {
  const logDir = path.join(workspacePath, ".logs");
  fs.mkdirSync(logDir, { recursive: true, mode: 0o755 });
  const stream = fs.createWriteStream(path.join(logDir, "claude.log"), {
    flags: "w",
  });
  return {
    write(data: unknown) {
      stream.write(JSON.stringify(data) + "\n");
    },
    close() {
      stream.end();
    },
  };
}

export function formatDuration(ms: number): string {
  const totalSeconds = Math.floor(ms / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return `${minutes}m${seconds}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h${minutes % 60}m${seconds}s`;
}
