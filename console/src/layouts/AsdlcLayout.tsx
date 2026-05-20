import { useEffect, useMemo, useState } from 'react';
import { matchPath, Outlet, useLocation, useNavigate, useParams } from 'react-router-dom';
import {
  AppShell,
  Box,
  Button,
  ColorSchemeToggle,
  ComplexSelect,
  Footer,
  Header,
  IconButton,
  Menu,
  MenuItem,
  Sidebar,
  Stack,
  UserMenu,
  useTheme,
} from '@wso2/oxygen-ui';
import {
  Building,
  ChevronRight,
  ChevronRightCircle,
  Compass,
  FlaskConical,
  LayoutDashboard,
  Package,
  Plus,
  Rocket,
  ScrollText,
  Sparkles,
  X,
  ClipboardList,
  Settings,
} from '@wso2/oxygen-ui-icons-react';
import { useUserClaims } from '../auth';
import { api } from '../services/api';
// MOCK: AI Chat panel
import ChatPanel from '../components/ChatPanel';
import { subscribeCopilotRequest } from '../services/chatStore';
import {
  organizationOverviewPath,
  projectOverviewPath,
  projectRequirementsPath,
  projectArchitecturePath,
  projectTasksPath,
  componentDetailPath,
  componentBuildPath,
  componentDeployPath,
  componentConfigsPath,
  componentTestPath,
  projectCreatePath,
} from '../lib/paths';
import { resolveOuHandle } from '../utils/orgClaims';

export default function AsdlcLayout() {
  const navigate = useNavigate();
  const location = useLocation();
  const theme = useTheme();
  const { orgId, projectId, componentId } = useParams();
  const { claims } = useUserClaims();
  const user = { name: claims?.name || 'User', email: claims?.email || '' };

  // Org identity: use JWT claims (ouHandle) which matches the OC namespace.
  // Same precedence as the BFF (asdlc-service/middleware/jwt.ResolveOuHandle).
  // No silent fallback — the route param (`:orgId` from useParams) is the
  // canonical org for the page; the JWT claim only seeds the org switcher.
  const claimsOrgId = resolveOuHandle(claims);
  const claimsOrgName = claims?.ouName || claimsOrgId;

  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem('asdlc:sidebarCollapsed') === '1';
    } catch {
      return false;
    }
  });

  useEffect(() => {
    try {
      localStorage.setItem('asdlc:sidebarCollapsed', collapsed ? '1' : '0');
    } catch {
      // ignore quota/access errors
    }
  }, [collapsed]);
  const [projectAnchorEl, setProjectAnchorEl] = useState<null | HTMLElement>(null);
  const [componentAnchorEl, setComponentAnchorEl] = useState<null | HTMLElement>(null);
  const projectMenuOpen = Boolean(projectAnchorEl);
  const componentMenuOpen = Boolean(componentAnchorEl);

  // MOCK: Copilot panel open/close state — managed here, passed to ChatPanel
  const [copilotOpen, setCopilotOpen] = useState(false);

  // MOCK: Track chat panel state for content shrinking (synced from ChatPanel)

  // MOCK: Listen for copilot open requests from pages
  useEffect(() => subscribeCopilotRequest(() => setCopilotOpen(true)), []);

  // routeOrgId resolves the org for the current page. Precedence:
  //   1. `:orgId` URL param — set on every project/component sub-route.
  //   2. JWT claim `ouHandle` — used on any org-less page.
  // If both are missing, App.tsx's `/` route renders NoOrganizationPage.
  const resolvedOrgId = orgId ?? claimsOrgId;
  const routeOrgId = resolvedOrgId ?? '';
  const inProjectLevel = Boolean(projectId);
  const inComponentLevel = Boolean(projectId && componentId);

  // Fetch projects and components from API
  const [projects, setProjects] = useState<import('../services/api/types').Project[]>([]);
  const [components, setComponents] = useState<import('../services/api/types').ComponentDefinition[]>([]);
  const [orgs, setOrgs] = useState<import('../services/api/types').Organization[]>([]);

  useEffect(() => {
    if (!routeOrgId) return;
    api.listProjects(routeOrgId).then(setProjects);
  }, [routeOrgId, location.pathname]);

  // Org list — refreshed when the route changes (e.g. after creating a new org).
  // On fetch failure orgs stays empty and the switcher falls back to the JWT
  // claim's org so the layout never breaks.
  useEffect(() => {
    api.listOrganizations().then(setOrgs);
  }, [location.pathname]);

  const selectedOrg = useMemo(() => orgs.find((o) => o.name === routeOrgId), [orgs, routeOrgId]);
  const selectedOrgName = selectedOrg?.displayName || selectedOrg?.name || claimsOrgName;

  const selectedProject = useMemo(() => {
    if (!projectId) return undefined;
    return projects.find((p) => p.id === projectId);
  }, [projects, projectId]);

  useEffect(() => {
    if (!projectId || !routeOrgId) { setComponents([]); return; }
    api.listComponents(routeOrgId, projectId).then(setComponents);
  }, [routeOrgId, projectId, location.pathname]);

  const selectedComponent = useMemo(() => {
    if (!componentId) return undefined;
    return components.find((c) => c.id === componentId);
  }, [components, componentId]);

  // Path helpers
  const orgPath = organizationOverviewPath(routeOrgId);
  const projectPath = projectId ? projectOverviewPath(routeOrgId, projectId) : orgPath;

  // Path helpers for component-level navigation
  const hasComponentRouteParams = Boolean(orgId && projectId && componentId);
  const routeComponentId = componentId ?? '';
  const componentOverviewRoute = hasComponentRouteParams
    ? componentDetailPath(routeOrgId, projectId!, routeComponentId)
    : '';
  const componentBuildRoute = hasComponentRouteParams
    ? componentBuildPath(routeOrgId, projectId!, routeComponentId)
    : '';
  const componentDeployRoute = hasComponentRouteParams
    ? componentDeployPath(routeOrgId, projectId!, routeComponentId)
    : '';
  const componentConfigsRoute = hasComponentRouteParams
    ? componentConfigsPath(routeOrgId, projectId!, routeComponentId)
    : '';
  const componentTestRoute = hasComponentRouteParams
    ? componentTestPath(routeOrgId, projectId!, routeComponentId)
    : '';

  // Determine active sidebar item based on current route
  const activeSidebarItem = (() => {
    // Component-level navigation
    if (inComponentLevel) {
      if (
        matchPath('/organizations/:orgId/projects/:projectId/components/:componentId/build', location.pathname)
      ) {
        return 'build';
      }
      if (
        matchPath('/organizations/:orgId/projects/:projectId/components/:componentId/deploy', location.pathname)
      ) {
        return 'deploy';
      }
      if (
        matchPath('/organizations/:orgId/projects/:projectId/components/:componentId/configs', location.pathname)
      ) {
        return 'configs';
      }
      if (
        matchPath('/organizations/:orgId/projects/:projectId/components/:componentId/test', location.pathname)
      ) {
        return 'test';
      }
      return 'overview';
    }

    // Org-level: settings vs overview.
    if (!projectId) {
      if (matchPath('/organizations/:orgId/settings/*', location.pathname)) {
        return 'settings';
      }
      return 'overview';
    }
    if (
      matchPath('/organizations/:orgId/projects/:projectId/requirements', location.pathname)
    ) {
      return 'requirements';
    }
    if (
      matchPath('/organizations/:orgId/projects/:projectId/architecture', location.pathname)
    ) {
      return 'architecture';
    }
    if (
      matchPath('/organizations/:orgId/projects/:projectId/tasks/*', location.pathname)
    ) {
      return 'tasks';
    }
    return 'overview';
  })();

  const handleSidebarSelect = (id: string) => {
    // Component-level navigation
    if (inComponentLevel && hasComponentRouteParams) {
      switch (id) {
        case 'overview':
          navigate(componentOverviewRoute);
          return;
        case 'build':
          navigate(componentBuildRoute);
          return;
        case 'deploy':
          navigate(componentDeployRoute);
          return;
        case 'configs':
          navigate(componentConfigsRoute);
          return;
        case 'test':
          navigate(componentTestRoute);
          return;
        default:
          return;
      }
    }

    // Organization-level navigation
    if (!projectId) {
      if (id === 'overview') {
        navigate(organizationOverviewPath(routeOrgId));
      }
      if (id === 'settings') {
        navigate(`/organizations/${routeOrgId}/settings`);
      }
      return;
    }

    // Project-level navigation
    switch (id) {
      case 'overview':
        navigate(projectOverviewPath(routeOrgId, projectId));
        break;
      case 'requirements':
        navigate(projectRequirementsPath(routeOrgId, projectId));
        break;
      case 'architecture':
        navigate(projectArchitecturePath(routeOrgId, projectId));
        break;
      case 'tasks':
        navigate(projectTasksPath(routeOrgId, projectId));
        break;
      default:
        break;
    }
  };

  const handleLogout = () => {
    // The Asgardeo SDK's signOut() for AsgardeoV2 platform redirects to
    // signInUrl (Thunder's /gate) without the required applicationId param,
    // causing an error on Thunder's gate page. Instead, clear the session
    // and navigate to /login which triggers the proper OAuth2 sign-in flow.
    localStorage.clear();
    window.location.href = '/login';
  };

  const renderOrgValue = () => (
    <>
      <ComplexSelect.MenuItem.Icon>
        <Building size={16} />
      </ComplexSelect.MenuItem.Icon>
      <ComplexSelect.MenuItem.Text primary={selectedOrgName} />
    </>
  );

  return (
    <>
    <AppShell>
      <AppShell.Navbar>
        <Header>
          <Header.Toggle collapsed={collapsed} onToggle={() => setCollapsed((prev) => !prev)} />

          <Header.Brand>
            <Stack
              direction="column"
              onClick={() => navigate(organizationOverviewPath(routeOrgId))}
              sx={{ cursor: 'pointer', lineHeight: 1 }}
            >
              <Box component="span" sx={{ fontWeight: 700, fontSize: '1.25rem', lineHeight: 1.1, whiteSpace: 'nowrap' }}>
                Agentic
              </Box>
              <Box component="span" sx={{ fontSize: '0.95rem', lineHeight: 1.1, whiteSpace: 'nowrap' }}>
                <Box component="span" sx={{ fontWeight: 700 }}>Engineering</Box>
                <Box component="span" sx={{ fontWeight: 400 }}> Platform</Box>
              </Box>
            </Stack>
          </Header.Brand>

          <Header.Switchers showDivider={false}>
            <Stack direction="row" alignItems="center" gap={0.5}>
              {/* Organization selector -- always visible */}
              <ComplexSelect
                value={routeOrgId}
                onChange={(event) => {
                  const value = String(event.target.value);
                  if (value && orgs.some((o) => o.name === value)) {
                    navigate(organizationOverviewPath(value));
                  }
                }}
                size="small"
                sx={{ minWidth: 220 }}
                renderValue={renderOrgValue}
                label="Organizations"
              >
                {(orgs.length > 0 ? orgs : [{ name: claimsOrgId, displayName: claimsOrgName, uuid: '', createdAt: '' }]).map((org) => (
                  // onClick fires on every click (including the currently-selected
                  // item), so clicking the active org in the dropdown lands on the
                  // org overview — onChange alone only fires on a value change.
                  <ComplexSelect.MenuItem
                    key={org.name}
                    value={org.name}
                    onClick={() => {
                      if (org.name) navigate(organizationOverviewPath(org.name));
                    }}
                  >
                    <ComplexSelect.MenuItem.Icon>
                      <Building size={16} />
                    </ComplexSelect.MenuItem.Icon>
                    <ComplexSelect.MenuItem.Text primary={org.displayName || org.name} />
                  </ComplexSelect.MenuItem>
                ))}
              </ComplexSelect>

              {/* Project selector -- shown when a project is selected, or as a chevron picker */}
              {selectedProject ? (
                <Box position="relative">
                  <ComplexSelect
                    value={projectId}
                    onChange={(event) => {
                      const value = String(event.target.value);
                      if (value && projects.some((p) => p.id === value)) {
                        navigate(projectOverviewPath(routeOrgId, value));
                      }
                    }}
                    size="small"
                    sx={{ minWidth: 190 }}
                    renderValue={() => (
                      <>
                        <ComplexSelect.MenuItem.Icon>
                          <ScrollText size={16} />
                        </ComplexSelect.MenuItem.Icon>
                        <ComplexSelect.MenuItem.Text primary={selectedProject.name} />
                      </>
                    )}
                    label="Projects"
                  >
                    <ComplexSelect.MenuItem
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                        navigate(projectCreatePath(routeOrgId));
                      }}
                    >
                      <ComplexSelect.MenuItem.Icon>
                        <Plus size={16} />
                      </ComplexSelect.MenuItem.Icon>
                      <ComplexSelect.MenuItem.Text primary="Create a Project" />
                    </ComplexSelect.MenuItem>
                    {projects.map((project) => (
                      <ComplexSelect.MenuItem
                        key={project.id}
                        value={project.id}
                        onClick={() => navigate(projectOverviewPath(routeOrgId, project.id))}
                      >
                        <ComplexSelect.MenuItem.Icon>
                          <ScrollText size={16} />
                        </ComplexSelect.MenuItem.Icon>
                        <ComplexSelect.MenuItem.Text
                          primary={project.name}
                          secondary={project.phase}
                        />
                      </ComplexSelect.MenuItem>
                    ))}
                  </ComplexSelect>
                  <Box position="absolute" right={0} top={-2}>
                    <IconButton
                      size="small"
                      sx={{ color: theme.vars?.palette.text.disabled }}
                      onClick={() => navigate(orgPath)}
                    >
                      <X size={12} />
                    </IconButton>
                  </Box>
                </Box>
              ) : (
                <>
                  <IconButton
                    onClick={(e) => setProjectAnchorEl(e.currentTarget)}
                    size="small"
                    sx={{
                      transform: projectMenuOpen ? 'rotate(90deg)' : 'rotate(0deg)',
                      transition: 'transform 0.2s',
                    }}
                  >
                    <ChevronRightCircle size={20} />
                  </IconButton>
                  <Menu
                    anchorEl={projectAnchorEl}
                    open={projectMenuOpen}
                    onClose={() => setProjectAnchorEl(null)}
                  >
                    <MenuItem
                      onClick={() => {
                        setProjectAnchorEl(null);
                        navigate(projectCreatePath(routeOrgId));
                      }}
                    >
                      <Plus size={16} style={{ marginRight: 8 }} />
                      Create a Project
                    </MenuItem>
                    {projects.map((project) => (
                      <MenuItem
                        key={project.id}
                        onClick={() => {
                          setProjectAnchorEl(null);
                          navigate(projectOverviewPath(routeOrgId, project.id));
                        }}
                      >
                        <ScrollText size={16} style={{ marginRight: 8 }} />
                        {project.name}
                      </MenuItem>
                    ))}
                  </Menu>
                </>
              )}

              {/* Component selector -- shown when inside a project */}
              {inProjectLevel && (
                <>
                  {selectedComponent ? (
                    <Box position="relative">
                      <ComplexSelect
                        value={componentId}
                        onChange={(event) =>
                          navigate(
                            componentDetailPath(routeOrgId, projectId!, String(event.target.value))
                          )
                        }
                        size="small"
                        sx={{ minWidth: 190 }}
                        renderValue={() => (
                          <>
                            <ComplexSelect.MenuItem.Icon>
                              <Package size={16} />
                            </ComplexSelect.MenuItem.Icon>
                            <ComplexSelect.MenuItem.Text primary={selectedComponent.name} />
                          </>
                        )}
                        label="Components"
                      >
                        {components.map((component) => (
                          <ComplexSelect.MenuItem
                            key={component.id}
                            value={component.id}
                            onClick={() =>
                              navigate(componentDetailPath(routeOrgId, projectId!, component.id))
                            }
                          >
                            <ComplexSelect.MenuItem.Icon>
                              <Package size={16} />
                            </ComplexSelect.MenuItem.Icon>
                            <ComplexSelect.MenuItem.Text
                              primary={component.name}
                              secondary={component.status}
                            />
                          </ComplexSelect.MenuItem>
                        ))}
                      </ComplexSelect>
                      <Box position="absolute" right={0} top={-2}>
                        <IconButton
                          size="small"
                          sx={{ color: theme.vars?.palette.text.disabled }}
                          onClick={() => navigate(projectPath)}
                        >
                          <X size={12} />
                        </IconButton>
                      </Box>
                    </Box>
                  ) : (
                    <>
                      {components.length > 0 && (
                        <>
                          <IconButton
                            onClick={(e) => setComponentAnchorEl(e.currentTarget)}
                            size="small"
                            sx={{
                              transform: componentMenuOpen ? 'rotate(90deg)' : 'rotate(0deg)',
                              transition: 'transform 0.2s',
                            }}
                          >
                            <ChevronRightCircle size={20} />
                          </IconButton>
                          <Menu
                            anchorEl={componentAnchorEl}
                            open={componentMenuOpen}
                            onClose={() => setComponentAnchorEl(null)}
                          >
                            {components.map((component) => (
                              <MenuItem
                                key={component.id}
                                onClick={() => {
                                  setComponentAnchorEl(null);
                                  navigate(
                                    componentDetailPath(routeOrgId, projectId!, component.id)
                                  );
                                }}
                              >
                                <Package size={16} style={{ marginRight: 8 }} />
                                {component.name}
                              </MenuItem>
                            ))}
                          </Menu>
                        </>
                      )}
                    </>
                  )}
                </>
              )}
            </Stack>
          </Header.Switchers>

          <Header.Spacer />

          <Header.Actions>
            {/* MOCK: Copilot toggle — shown on project-level pages, next to System Mode */}
            {inProjectLevel && (
              <IconButton
                size="small"
                onClick={() => setCopilotOpen((prev) => !prev)}
                sx={{
                  color: copilotOpen
                    ? theme.vars?.palette.primary.main ?? 'primary.main'
                    : undefined,
                }}
                aria-label="Copilot"
              >
                <Sparkles size={20} />
              </IconButton>
            )}
            <ColorSchemeToggle />
            <UserMenu
              user={{ name: user.name, email: user.email }}
              onLogout={handleLogout}
            />
          </Header.Actions>
        </Header>
      </AppShell.Navbar>

      <AppShell.Sidebar>
        <Sidebar
          collapsed={collapsed}
          activeItem={activeSidebarItem}
          onSelect={handleSidebarSelect}
        >
          <Sidebar.Nav>
            {/* Organization-level sidebar */}
            {!inProjectLevel && (
              <Sidebar.Category>
                <Sidebar.Item id="overview">
                  <Sidebar.ItemIcon>
                    <LayoutDashboard size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Overview</Sidebar.ItemLabel>
                </Sidebar.Item>
                <Sidebar.Item id="settings">
                  <Sidebar.ItemIcon>
                    <Settings size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Settings</Sidebar.ItemLabel>
                </Sidebar.Item>
              </Sidebar.Category>
            )}

            {/* Project-level sidebar (not inside a component) */}
            {inProjectLevel && !inComponentLevel && (
              <Sidebar.Category>
                <Sidebar.Item id="overview">
                  <Sidebar.ItemIcon>
                    <LayoutDashboard size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Overview</Sidebar.ItemLabel>
                </Sidebar.Item>
                <Sidebar.Item id="requirements">
                  <Sidebar.ItemIcon>
                    <ScrollText size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Requirements</Sidebar.ItemLabel>
                </Sidebar.Item>
                <Sidebar.Item id="architecture">
                  <Sidebar.ItemIcon>
                    <Compass size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Design</Sidebar.ItemLabel>
                </Sidebar.Item>
                <Sidebar.Item id="tasks">
                  <Sidebar.ItemIcon>
                    <ClipboardList size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Implementation</Sidebar.ItemLabel>
                </Sidebar.Item>
              </Sidebar.Category>
            )}

            {/* Component-level sidebar */}
            {inComponentLevel && (
              <Sidebar.Category>
                <Sidebar.Item id="overview">
                  <Sidebar.ItemIcon>
                    <LayoutDashboard size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Overview</Sidebar.ItemLabel>
                </Sidebar.Item>
                <Sidebar.Item id="build">
                  <Sidebar.ItemIcon>
                    <Package size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Build</Sidebar.ItemLabel>
                </Sidebar.Item>
                <Sidebar.Item id="configs">
                  <Sidebar.ItemIcon>
                    <Settings size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Configs</Sidebar.ItemLabel>
                </Sidebar.Item>
                <Sidebar.Item id="deploy">
                  <Sidebar.ItemIcon>
                    <Rocket size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Deploy</Sidebar.ItemLabel>
                </Sidebar.Item>
                <Sidebar.Item id="test">
                  <Sidebar.ItemIcon>
                    <FlaskConical size={20} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>Test</Sidebar.ItemLabel>
                </Sidebar.Item>
              </Sidebar.Category>
            )}
          </Sidebar.Nav>

          <Sidebar.Footer>
            <Sidebar.Category>
              <Button
                variant="text"
                fullWidth
                onClick={() => setCollapsed((prev) => !prev)}
                sx={{ minHeight: 'auto', py: 1, justifyContent: 'flex-start' }}
              >
                <Sidebar.Item id="expand">
                  <Sidebar.ItemIcon>
                    <ChevronRight size={20} style={{ transform: collapsed ? 'none' : 'rotate(180deg)' }} />
                  </Sidebar.ItemIcon>
                  <Sidebar.ItemLabel>{collapsed ? 'Expand' : 'Collapse'}</Sidebar.ItemLabel>
                </Sidebar.Item>
              </Button>
            </Sidebar.Category>
          </Sidebar.Footer>
        </Sidebar>
      </AppShell.Sidebar>

      <AppShell.Main>
        <Box sx={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'row', overflow: 'hidden' }}>
          <Box sx={{ flex: 1, minWidth: 0, overflow: 'auto' }}>
            <Outlet />
          </Box>
          {inProjectLevel && (
            <Box
              sx={{
                width: copilotOpen ? 380 : 0,
                flexShrink: 0,
                overflow: 'hidden',
                transition: 'width 0.22s ease-out',
              }}
            >
              <ChatPanel onClose={() => setCopilotOpen(false)} />
            </Box>
          )}
        </Box>
      </AppShell.Main>

      <AppShell.Footer>
        <Footer
          copyright={`\u00A9 ${new Date().getFullYear()} Agentic Engineering. All rights reserved.`}
          termsUrl="#terms"
          privacyUrl="#privacy"
          sx={{ py: 0.5 }}
        />
      </AppShell.Footer>

    </AppShell>
    </>
  );
}
