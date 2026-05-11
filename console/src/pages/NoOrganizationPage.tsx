import { Box, Button, PageContent, Stack, Typography } from '@wso2/oxygen-ui';

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
 *
 * Org creation is intentionally not offered here — orgs are provisioned
 * out-of-band (Thunder signup → platform-api-service in hosted;
 * seed-admin-org.sh in local dev). The BFF is read-only over OC
 * namespaces; see asdlc-service/controllers/organization_controller.go.
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
          your administrator to get onboarded.
        </Typography>
        <Box sx={{ display: 'flex', gap: 2 }}>
          <Button variant="outlined" onClick={handleSignOut}>
            Sign out
          </Button>
        </Box>
      </Stack>
    </PageContent>
  );
}
