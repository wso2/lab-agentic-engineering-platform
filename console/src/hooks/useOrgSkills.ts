import { useQuery } from '@tanstack/react-query';
import { orgSkillsApi, type SkillSummary } from '../services/api/orgSkills';

/**
 * useOrgSkills fetches the org's skills catalogue (built-ins + custom +
 * imported). Drives the Settings → Skills list view. Mutations invalidate
 * `['orgSkills', orgId]` to refetch.
 *
 * See docs/design/skills-system.md > "REST API".
 */
export function useOrgSkills(orgId: string | undefined) {
  return useQuery<SkillSummary[]>({
    queryKey: ['orgSkills', orgId],
    queryFn: () => orgSkillsApi.list(orgId as string),
    enabled: !!orgId,
    staleTime: 30 * 1000,
  });
}

/** Stable query key for cache invalidation after a mutation. */
export const orgSkillsQueryKey = (orgId: string | undefined) => ['orgSkills', orgId];
