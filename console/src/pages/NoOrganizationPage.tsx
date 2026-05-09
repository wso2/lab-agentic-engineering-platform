import { Box, Button, PageContent, Stack, Typography } from '@wso2/oxygen-ui';
import { organizationCreatePath } from '../lib/paths';

/**
 * Rendered when an authenticated user's JWT has no organization claim
 * (`ouHandle`, `ouName`, or `ouId`). Three legitimate failure modes
 * land here:
 *
 *   1. Pre-onboarded user — IDP issued a JWT with `sub` but the user
 *      hasn't been assigned to any organization.
 *   2. Misconfigured OAuth app — admin forgot to enable the org-claim
 *      mapping.
 *   3. M2M token leaked into a browser context — `client_credentials`
 *      tokens have no `ouHandle` because they have no human user.
 *
 * We do NOT silently fall back to a placeholder org; that hid all
 * three modes behind the same "everything works" UX.
 */
export default function NoOrganizationPage() {
  const handleSignOut = () => {
    localStorage.clear();
    window.location.href = '/login';
  };

  return (
    <PageContent>
      <Stack spacing={2} sx={{ maxWidth: 560, mx: 'auto', mt: 8 }}>
        <Typography variant="h4">No organization assigned</Typography>
        <Typography variant="body1" color="text.secondary">
          Your account has not been assigned to an organization. Contact
          your administrator if you expect to see one, or create a new
          organization to get started.
        </Typography>
        <Box sx={{ display: 'flex', gap: 2 }}>
          <Button variant="contained" href={organizationCreatePath()}>
            Create organization
          </Button>
          <Button variant="outlined" onClick={handleSignOut}>
            Sign out
          </Button>
        </Box>
      </Stack>
    </PageContent>
  );
}
