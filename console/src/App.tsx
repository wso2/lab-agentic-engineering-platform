import { useMemo } from 'react';
import { Navigate, Outlet, Route, Routes } from 'react-router-dom';
import { useAuth, useUserClaims } from './auth';
import AuthGuard from './auth/AuthGuard';
import { setTokenAccessor } from './services/api/rest';
import AsdlcLayout from './layouts/AsdlcLayout';
import OrgOverviewPage from './pages/OrgOverviewPage';
import OrganizationCreatePage from './pages/OrganizationCreatePage';
import ProjectCreatePage from './pages/ProjectCreatePage';
import ProjectPromptPage from './pages/ProjectPromptPage';
import ProjectArchitecturePage from './pages/ProjectArchitecturePage';
import ProjectTasksPage from './pages/ProjectTasksPage';
import ProjectRequirementsPage from './pages/ProjectRequirementsPage';
import ProjectOverviewPage from './pages/ProjectOverviewPage';
import ProjectComponentsPage from './pages/ProjectComponentsPage';
import ComponentDetailPage from './pages/ComponentDetailPage';
import ComponentBuildPage from './pages/ComponentBuildPage';
import ComponentDeployPage from './pages/ComponentDeployPage';
import ComponentConfigsPage from './pages/ComponentConfigsPage';
import OrgSettingsLayout from './pages/OrgSettingsLayout';
import OrgGitHubSettings from './pages/OrgGitHubSettings';
import OrgGitHubAppPicker from './pages/OrgGitHubAppPicker';
import { setOrgGithubTokenAccessor } from './services/api/orgGithub';
import { organizationOverviewPath } from './lib/paths';

export function App() {
  const { isSignedIn, getAccessToken } = useAuth();
  const { claims, isLoading: isClaimsLoading } = useUserClaims();

  if (isSignedIn) {
    setTokenAccessor(getAccessToken);
    setOrgGithubTokenAccessor(getAccessToken);
  } else {
    setTokenAccessor(null);
    setOrgGithubTokenAccessor(null);
  }

  const orgId = useMemo(() => {
    if (!claims) return 'default';
    return claims.ouHandle || claims.ouName || claims.ouId || 'default';
  }, [claims]);

  const defaultOrgPath = organizationOverviewPath(orgId);

  if (isSignedIn && isClaimsLoading) {
    return null;
  }

  return (
    <AuthGuard>
      <Routes>
        <Route path="/login" element={<Navigate to={defaultOrgPath} replace />} />

      <Route element={isSignedIn ? <AsdlcLayout /> : <Navigate to="/login" replace />}>
        <Route path="/" element={<Navigate to={defaultOrgPath} replace />} />
        <Route path="/organizations/new" element={<OrganizationCreatePage />} />
        <Route path="/organizations/:orgId" element={<OrgOverviewPage />} />
        <Route path="/organizations/:orgId/projects/new" element={<ProjectCreatePage />} />

        {/* Org Settings → GitHub Integration */}
        <Route path="/organizations/:orgId/settings" element={<OrgSettingsLayout />}>
          <Route index element={<Navigate to="github" replace />} />
          <Route path="github" element={<OrgGitHubSettings />} />
          <Route path="github/pick" element={<OrgGitHubAppPicker />} />
        </Route>

        <Route path="/organizations/:orgId/projects/:projectId" element={<Outlet />}>
          <Route index element={<ProjectOverviewPage />} />
          <Route path="prompt" element={<ProjectPromptPage />} />
          <Route path="requirements" element={<ProjectRequirementsPage />} />
          <Route path="architecture" element={<ProjectArchitecturePage />} />
          <Route path="tasks" element={<ProjectTasksPage />} />
          <Route path="implementation-plan" element={<Navigate to="../tasks" replace />} />
          <Route path="implementation" element={<Navigate to="../tasks" replace />} />
          <Route path="spec" element={<Navigate to="../requirements" replace />} />
          <Route path="design" element={<Navigate to="../requirements" replace />} />
          <Route path="components" element={<ProjectComponentsPage />} />

          <Route path="components/:componentId" element={<Outlet />}>
            <Route index element={<ComponentDetailPage />} />
            <Route path="build" element={<ComponentBuildPage />} />
            <Route path="deploy" element={<ComponentDeployPage />} />
            <Route path="configs" element={<ComponentConfigsPage />} />
          </Route>
        </Route>
      </Route>

        <Route path="*" element={<Navigate to={defaultOrgPath} replace />} />
      </Routes>
    </AuthGuard>
  );
}
