import { useQuery } from '@tanstack/react-query';
import { orgAnthropicApi, type OrgAnthropicProjection } from '../services/api/orgAnthropic';

/**
 * useOrgAnthropic fetches the per-org Anthropic projection.
 *
 * Used by the tasks-page header to gate the "Implement via Remote Agents"
 * action — disabled with a tooltip pointing at org settings when the
 * status is not `active`. The actual key bytes never reach the console;
 * this hook only sees the prefix / last4 / status projection.
 *
 * See docs/design/anthropic-key-dual-token.md §2.2.
 */
export function useOrgAnthropic(orgId: string | undefined) {
  return useQuery<OrgAnthropicProjection>({
    queryKey: ['orgAnthropic', orgId],
    queryFn: () => orgAnthropicApi.getStatus(orgId as string),
    enabled: !!orgId,
    // Soft TTL — projection changes via the settings page; the user is
    // unlikely to flip back to the tasks page faster than a fresh fetch.
    staleTime: 60 * 1000,
  });
}
