import { lazy, Suspense, useMemo } from 'react';
import { Navigate, Outlet, Route, Routes, useOutletContext } from 'react-router-dom';
import { useAuth, useUserClaims } from './auth';
import AuthGuard from './auth/AuthGuard';
import { setTokenAccessor } from './services/api/rest';
import AsdlcLayout from './layouts/AsdlcLayout';
import OrgOverviewPage from './pages/OrgOverviewPage';
import ProjectCreatePage from './pages/ProjectCreatePage';
import ProjectArchitecturePage from './pages/ProjectArchitecturePage';
import ProjectTasksPage from './pages/ProjectTasksPage';
import TaskDetailPage from './pages/TaskDetailPage';
import ProjectRequirementsPage from './pages/ProjectRequirementsPage';
import ProjectOverviewPage from './pages/ProjectOverviewPage';
import ComponentDetailPage from './pages/ComponentDetailPage';
import ComponentBuildPage from './pages/ComponentBuildPage';
import ComponentDeployPage from './pages/ComponentDeployPage';
import ComponentConfigsPage from './pages/ComponentConfigsPage';
// Lazy-load swagger-ui (~1MB) so it only ships when the Test tab is opened.
const ComponentTestPage = lazy(() => import('./pages/ComponentTestPage'));
import OrgSettingsLayout from './pages/OrgSettingsLayout';
import OrgGitHubSettings from './pages/OrgGitHubSettings';
import OrgGitHubAppPicker from './pages/OrgGitHubAppPicker';
import NoOrganizationPage from './pages/NoOrganizationPage';
import { setOrgGithubTokenAccessor } from './services/api/orgGithub';
import { organizationOverviewPath } from './lib/paths';
import { resolveOuHandle } from './utils/orgClaims';

// Forwards the parent layout's outlet context (e.g. setSidebarCollapsed) through
// nested route boundaries so deep pages can still call useOutletContext().
function ContextForwardingOutlet() {
  const context = useOutletContext();
  return <Outlet context={context} />;
}

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

  // Canonical OC org handle from the JWT claims, with the same precedence
  // the BFF uses (asdlc-service/middleware/jwt.ResolveOuHandle). Returns
  // undefined when the token has none of `ouHandle`/`ouName`/`ouId`; we
  // surface that as a fail-loud "no organization" page rather than
  // silently substituting a placeholder org.
  const orgId = useMemo(() => resolveOuHandle(claims), [claims]);

  // When the user has no org claim, land on `/` which renders NoOrganizationPage.
  // Org creation happens out-of-band via Thunder/platform-api-service — not in the BFF.
  const defaultLandingPath = orgId ? organizationOverviewPath(orgId) : '/';

  if (isSignedIn && isClaimsLoading) {
    return null;
  }

  return (
    <AuthGuard>
      <Routes>
        <Route path="/login" element={<Navigate to={defaultLandingPath} replace />} />

      <Route element={isSignedIn ? <AsdlcLayout /> : <Navigate to="/login" replace />}>
        <Route
          path="/"
          element={isSignedIn && !orgId ? <NoOrganizationPage /> : <Navigate to={defaultLandingPath} replace />}
        />
        <Route path="/organizations/:orgId" element={<OrgOverviewPage />} />
        <Route path="/organizations/:orgId/projects/new" element={<ProjectCreatePage />} />

        {/* Org Settings → GitHub Integration */}
        <Route path="/organizations/:orgId/settings" element={<OrgSettingsLayout />}>
          <Route index element={<Navigate to="github" replace />} />
          <Route path="github" element={<OrgGitHubSettings />} />
          <Route path="github/pick" element={<OrgGitHubAppPicker />} />
        </Route>

        <Route path="/organizations/:orgId/projects/:projectId" element={<ContextForwardingOutlet />}>
          <Route index element={<ProjectOverviewPage />} />
          <Route path="requirements" element={<ProjectRequirementsPage />} />
          <Route path="architecture" element={<ProjectArchitecturePage />} />
          <Route path="tasks" element={<ProjectTasksPage />} />
          <Route path="tasks/:taskId" element={<TaskDetailPage />} />
          <Route path="implementation-plan" element={<Navigate to="../tasks" replace />} />
          <Route path="implementation" element={<Navigate to="../tasks" replace />} />
          <Route path="spec" element={<Navigate to="../requirements" replace />} />
          <Route path="design" element={<Navigate to="../requirements" replace />} />
          <Route path="prompt" element={<Navigate to=".." replace />} />
          <Route path="components" element={<Navigate to=".." replace />} />

          <Route path="components/:componentId" element={<ContextForwardingOutlet />}>
            <Route index element={<ComponentDetailPage />} />
            <Route path="build" element={<ComponentBuildPage />} />
            <Route path="deploy" element={<ComponentDeployPage />} />
            <Route path="configs" element={<ComponentConfigsPage />} />
            <Route
              path="test"
              element={
                <Suspense fallback={null}>
                  <ComponentTestPage />
                </Suspense>
              }
            />
          </Route>
        </Route>
      </Route>

        <Route path="*" element={<Navigate to={defaultLandingPath} replace />} />
      </Routes>
    </AuthGuard>
  );
}
