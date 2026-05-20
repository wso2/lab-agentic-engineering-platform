import { Stack, Typography, Link } from '@wso2/oxygen-ui';
import type { ComponentTask } from '../../services/api';

export function TaskArtifactsBar({ task }: { task: ComponentTask }) {
  const items: { label: string; href?: string; value?: string }[] = [];
  if (task.issueUrl) items.push({ label: `Issue #${task.issueNumber ?? ''}`.trim(), href: task.issueUrl });
  if (task.pullRequestUrl) items.push({ label: `PR #${task.pullRequestNumber ?? ''}`.trim(), href: task.pullRequestUrl });
  if (task.branchName) items.push({ label: `Branch ${task.branchName}`, value: task.branchName });
  if (task.mergeCommitSha) items.push({ label: `Merge ${task.mergeCommitSha.slice(0, 7)}`, value: task.mergeCommitSha });

  if (items.length === 0) {
    return (
      <Typography variant="caption" color="text.disabled">
        No GitHub artifacts yet — they will appear here once the task is dispatched.
      </Typography>
    );
  }

  return (
    <Stack direction="row" spacing={2} flexWrap="wrap" sx={{ rowGap: 1 }}>
      {items.map((it, i) => (
        <Typography key={i} variant="caption" sx={{ color: 'text.secondary' }}>
          {it.href ? (
            <Link href={it.href} target="_blank" rel="noopener noreferrer" underline="hover">
              {it.label}
            </Link>
          ) : (
            it.label
          )}
        </Typography>
      ))}
    </Stack>
  );
}
