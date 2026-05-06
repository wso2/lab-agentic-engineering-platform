import { useCallback, useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Avatar,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Grid,
  PageContent,
  Skeleton,
  Stack,
  TextField,
  Typography,
  useTheme,
} from '@wso2/oxygen-ui';
import { ArrowLeft, Check, Edit, Rocket } from '@wso2/oxygen-ui-icons-react';
import { api } from '../services/api';
import type { Design, DesignComponent, Project, Spec } from '../services/api';
import { organizationOverviewPath, projectOverviewPath } from '../lib/paths';

// ---------------------------------------------------------------------------
// Stepper indicator
// ---------------------------------------------------------------------------

interface StepIndicatorProps {
  activeStep: number;
  specApproved: boolean;
  designApproved: boolean;
  onStepClick: (step: number) => void;
}

function StepIndicator({ activeStep, specApproved, designApproved, onStepClick }: StepIndicatorProps) {
  const theme = useTheme();

  const steps = [
    { label: 'Specification', completed: specApproved },
    { label: 'Design', completed: designApproved },
  ];

  return (
    <Stack direction="row" alignItems="center" gap={1} sx={{ mb: 4 }}>
      {steps.map((step, index) => {
        const isActive = activeStep === index;
        const isClickable = index === 0 || specApproved;

        return (
          <Box key={step.label} sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
            {index > 0 && (
              <Box
                sx={{
                  width: 40,
                  height: 2,
                  bgcolor: step.completed || isActive
                    ? theme.vars?.palette.primary.main ?? 'primary.main'
                    : theme.vars?.palette.action?.disabledBackground ?? 'action.disabledBackground',
                  borderRadius: 1,
                }}
              />
            )}
            <Chip
              label={
                <Stack direction="row" alignItems="center" gap={0.5}>
                  {step.completed ? (
                    <Check size={14} />
                  ) : (
                    <Box
                      component="span"
                      sx={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        width: 18,
                        height: 18,
                        borderRadius: '50%',
                        fontSize: '0.7rem',
                        fontWeight: 700,
                        bgcolor: isActive
                          ? 'transparent'
                          : theme.vars?.palette.action?.disabledBackground ?? 'action.disabledBackground',
                        color: isActive ? 'inherit' : theme.vars?.palette.text?.secondary ?? 'text.secondary',
                      }}
                    >
                      {index + 1}
                    </Box>
                  )}
                  <span>{step.label}</span>
                </Stack>
              }
              color={isActive || step.completed ? 'primary' : 'default'}
              variant={isActive ? 'filled' : 'outlined'}
              size="medium"
              onClick={isClickable ? () => onStepClick(index) : undefined}
              sx={{
                cursor: isClickable ? 'pointer' : 'default',
                opacity: isClickable ? 1 : 0.5,
                fontWeight: isActive ? 600 : 400,
                px: 1,
              }}
            />
          </Box>
        );
      })}
    </Stack>
  );
}

// ---------------------------------------------------------------------------
// Requirements section — derived from spec
// ---------------------------------------------------------------------------

function RequirementsSection({ requirements }: { requirements: string[] }) {
  const theme = useTheme();

  if (!requirements || requirements.length === 0) return null;

  return (
    <Card variant="outlined" sx={{ mb: 3 }}>
      <CardContent>
        <Stack direction="row" alignItems="center" gap={1} sx={{ mb: 2 }}>
          <Typography variant="h6" sx={{ fontWeight: 600 }}>
            Derived Requirements
          </Typography>
          <Chip label={`${requirements.length}`} size="small" variant="outlined" />
        </Stack>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          The AI extracted these requirements from your specification. Each will guide component implementation.
        </Typography>
        <Stack gap={0}>
          {requirements.map((req, i) => (
            <Stack
              key={i}
              direction="row"
              alignItems="flex-start"
              gap={1.5}
              sx={{
                py: 1.25,
                px: 2,
                borderRadius: 1,
                bgcolor: i % 2 === 0 ? 'transparent' : 'action.hover',
              }}
            >
              <Box
                sx={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  width: 24,
                  height: 24,
                  borderRadius: '50%',
                  bgcolor: theme.vars?.palette.primary.main ?? 'primary.main',
                  color: theme.vars?.palette.primary.contrastText ?? '#fff',
                  fontSize: '0.7rem',
                  fontWeight: 700,
                  flexShrink: 0,
                  mt: 0.125,
                }}
              >
                {i + 1}
              </Box>
              <Typography variant="body2" sx={{ lineHeight: 1.7 }}>
                {req}
              </Typography>
            </Stack>
          ))}
        </Stack>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Component card — shows structured component design
// ---------------------------------------------------------------------------

function DesignComponentCard({ component, index }: { component: DesignComponent; index: number }) {
  const theme = useTheme();
  const [expanded, setExpanded] = useState(false);

  return (
    <Card
      variant="outlined"
      sx={{
        height: '100%',
        display: 'flex',
        flexDirection: 'column',
        transition: 'box-shadow 0.2s, border-color 0.2s',
        '&:hover': { boxShadow: 4, borderColor: 'primary.main' },
      }}
    >
      <CardContent sx={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
        {/* Header */}
        <Stack direction="row" alignItems="center" gap={1.5} sx={{ mb: 2 }}>
          <Avatar
            sx={{
              width: 40,
              height: 40,
              fontSize: 16,
              fontWeight: 700,
              bgcolor: theme.vars?.palette.primary.main ?? 'primary.main',
              color: theme.vars?.palette.primary.contrastText ?? '#fff',
            }}
          >
            {component.name[0]?.toUpperCase() ?? (index + 1)}
          </Avatar>
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Typography variant="subtitle1" sx={{ fontWeight: 700, lineHeight: 1.3 }} noWrap>
              {component.name}
            </Typography>
          </Box>
        </Stack>

        {/* Chips row */}
        <Stack direction="row" gap={0.75} flexWrap="wrap" sx={{ mb: 2 }}>
          <Chip
            label={component.entrypoint}
            size="small"
            variant="outlined"
            color="primary"
            sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}
          />
          <Chip
            label={component.buildpack}
            size="small"
            variant="outlined"
            sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}
          />
        </Stack>

        {/* Metadata fields */}
        <Stack gap={1.5} sx={{ flex: 1 }}>
          {/* App Path */}
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5 }}>
              App Path
            </Typography>
            <Box
              sx={{
                mt: 0.5,
                px: 1.5,
                py: 0.75,
                borderRadius: 1,
                bgcolor: 'action.hover',
                fontFamily: 'monospace',
                fontSize: '0.8rem',
              }}
            >
              {component.appPath}
            </Box>
          </Box>

          {/* OpenAPI Spec preview */}
          {component.openAPISpec && (
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5 }}>
              OpenAPI Spec
            </Typography>
            <Box
              sx={{
                mt: 0.5,
                px: 1.5,
                py: 1,
                borderRadius: 1,
                bgcolor: 'action.hover',
                fontFamily: 'monospace',
                fontSize: '0.75rem',
                lineHeight: 1.6,
                maxHeight: expanded ? 'none' : 120,
                overflow: 'hidden',
                position: 'relative',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
              }}
            >
              {component.openAPISpec}
              {!expanded && component.openAPISpec.length > 200 && (
                <Box
                  sx={{
                    position: 'absolute',
                    bottom: 0,
                    left: 0,
                    right: 0,
                    height: 40,
                    background: `linear-gradient(transparent, ${theme.vars?.palette.background?.paper ?? '#fff'})`,
                  }}
                />
              )}
            </Box>
            {component.openAPISpec.length > 200 && (
              <Button
                variant="text"
                size="small"
                onClick={() => setExpanded(!expanded)}
                sx={{ mt: 0.5, textTransform: 'none', fontSize: '0.75rem' }}
              >
                {expanded ? 'Show less' : 'Show full spec'}
              </Button>
            )}
          </Box>
          )}

          {/* Agent Instructions */}
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, textTransform: 'uppercase', letterSpacing: 0.5 }}>
              Agent Instructions
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, lineHeight: 1.7 }}>
              {component.componentAgentInstructions}
            </Typography>
          </Box>
        </Stack>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Components section — grid of design component cards
// ---------------------------------------------------------------------------

function ComponentsSection({ components }: { components: DesignComponent[] }) {
  if (!components || components.length === 0) return null;

  return (
    <Box>
      <Stack direction="row" alignItems="center" gap={1} sx={{ mb: 2 }}>
        <Typography variant="h6" sx={{ fontWeight: 600 }}>
          Components
        </Typography>
        <Chip label={`${components.length}`} size="small" variant="outlined" />
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2.5 }}>
        Each component will be created as a separate service in your project repository.
      </Typography>
      <Grid container spacing={2.5}>
        {components.map((comp, i) => (
          <Grid key={comp.name} size={{ xs: 12, md: 6 }}>
            <DesignComponentCard component={comp} index={i} />
          </Grid>
        ))}
      </Grid>
    </Box>
  );
}

// ---------------------------------------------------------------------------
// Main page component
// ---------------------------------------------------------------------------

export default function ProjectSpecPage() {
  const navigate = useNavigate();
  const { orgId, projectId } = useParams();
  const routeOrgId = orgId ?? 'default';

  // Data state
  const [project, setProject] = useState<Project | undefined>();
  const [spec, setSpec] = useState<Spec | undefined>();
  const [design, setDesign] = useState<Design | undefined>();
  const [loading, setLoading] = useState(true);

  // Spec editing state
  const [editing, setEditing] = useState(false);
  const [editContent, setEditContent] = useState('');

  // Design generation state
  const [generating, setGenerating] = useState(false);
  const [approving, setApproving] = useState(false);

  // Wizard step: 0 = Specification, 1 = Design
  const [activeStep, setActiveStep] = useState(0);

  const loadData = useCallback(async () => {
    if (!projectId) return;
    const [p, s, d] = await Promise.all([
      api.getProject(routeOrgId, projectId),
      api.getSpec(routeOrgId, projectId),
      api.getDesign(routeOrgId, projectId),
    ]);
    setProject(p);
    const resolvedSpec = s ?? (p ? {
      projectId,
      content: '',
      status: 'draft' as const,
      version: 0,
    } : undefined);
    setSpec(resolvedSpec);
    setDesign(d);

    if (resolvedSpec && !resolvedSpec.content) {
      setEditContent('');
      setEditing(true);
    }

    // If spec approved, go to design step
    if (s?.status === 'approved') {
      setActiveStep(1);
    }

    setLoading(false);
  }, [projectId, routeOrgId]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  // -- Confirm spec: save + approve + generate design in one action ----------

  const handleConfirmSpec = async () => {
    if (!projectId || !editContent.trim()) return;
    setGenerating(true);

    // 1. Save spec
    const saved = await api.updateSpec(routeOrgId, projectId, editContent);
    if (!saved) { setGenerating(false); return; }
    setSpec(saved);
    setEditing(false);

    // 2. Approve spec
    const approved = await api.approveSpec(routeOrgId, projectId);
    if (!approved) { setGenerating(false); return; }
    setSpec(approved);
    setActiveStep(1);

    // 3. Generate design
    const result = await api.generateDesign(routeOrgId, projectId);
    if (result) {
      setDesign(result);
    }
    setGenerating(false);
  };

  // -- Regenerate design (from design step) ----------------------------------

  const handleRegenerateDesign = async () => {
    if (!projectId) return;
    setGenerating(true);
    const result = await api.generateDesign(routeOrgId, projectId);
    if (result) {
      setDesign(result);
    }
    setGenerating(false);
  };

  // -- Edit spec (go back to editing) ----------------------------------------

  const startEditSpec = () => {
    if (spec) {
      setEditContent(spec.content);
      setEditing(true);
      setActiveStep(0);
    }
  };

  // -- Approve design --------------------------------------------------------

  const handleApproveDesign = async () => {
    if (!projectId) return;
    setApproving(true);
    const result = await api.approveDesign(routeOrgId, projectId);
    if (result) {
      setDesign(result);
      navigate(projectOverviewPath(routeOrgId, projectId));
    }
    setApproving(false);
  };

  // -- Step navigation -------------------------------------------------------

  const handleStepClick = (step: number) => {
    if (step === 1 && spec?.status !== 'approved') return;
    setActiveStep(step);
  };

  // -- Loading & error states ------------------------------------------------

  if (loading) {
    return (
      <PageContent>
        <Skeleton variant="text" width="40%" height={40} />
        <Skeleton variant="text" width="20%" height={24} sx={{ mt: 1 }} />
        <Skeleton variant="rectangular" width="100%" height={200} sx={{ mt: 3, borderRadius: 1 }} />
      </PageContent>
    );
  }

  if (!project) {
    return (
      <PageContent>
        <Typography variant="h5" color="error">
          Project not found
        </Typography>
        <Button
          variant="text"
          startIcon={<ArrowLeft size={16} />}
          onClick={() => navigate(organizationOverviewPath(routeOrgId))}
          sx={{ mt: 2 }}
        >
          Back to Projects
        </Button>
      </PageContent>
    );
  }

  if (!spec) {
    return (
      <PageContent>
        <Skeleton variant="text" width="40%" height={40} />
        <Skeleton variant="rectangular" width="100%" height={200} sx={{ mt: 3, borderRadius: 1 }} />
      </PageContent>
    );
  }

  const specApproved = spec.status === 'approved';
  const designApproved = design?.status === 'approved';
  const hasDesign = design && design.status !== 'none' && design.status !== 'generating';

  // -- Render: Step 1 — Specification ----------------------------------------

  const renderSpecStep = () => {
    if (editing) {
      return (
        <Box>
          <TextField
            value={editContent}
            onChange={(e) => setEditContent(e.target.value)}
            placeholder="Describe what you want to build. The AI will generate an architecture design from your specification."
            multiline
            minRows={8}
            maxRows={20}
            fullWidth
            disabled={generating}
            sx={{ mb: 2 }}
          />
          <Stack direction="row" justifyContent="flex-end" gap={1}>
            {spec.content && (
              <Button variant="outlined" onClick={() => setEditing(false)} disabled={generating}>
                Cancel
              </Button>
            )}
            <Button
              variant="contained"
              startIcon={<Rocket size={16} />}
              onClick={handleConfirmSpec}
              disabled={!editContent.trim() || generating}
            >
              {generating ? 'Generating Design...' : 'Generate Design'}
            </Button>
          </Stack>
        </Box>
      );
    }

    // Spec already saved but not yet approved — show it with confirm button
    return (
      <Box>
        <Box
          sx={{
            bgcolor: 'action.hover',
            borderRadius: 1,
            p: 3,
            mb: 3,
            whiteSpace: 'pre-wrap',
            fontFamily: 'monospace',
            fontSize: '0.9rem',
            lineHeight: 1.7,
          }}
        >
          {spec.content}
        </Box>
        <Stack direction="row" justifyContent="flex-end" gap={1}>
          <Button variant="outlined" startIcon={<Edit size={16} />} onClick={startEditSpec}>
            Edit
          </Button>
        </Stack>
      </Box>
    );
  };

  // -- Render: Step 2 — Design -----------------------------------------------

  const renderDesignStep = () => {
    if (generating) {
      return (
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 8 }}>
          <CircularProgress size={48} sx={{ mb: 3 }} />
          <Typography variant="h6" color="text.secondary">
            Generating design...
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
            Analyzing your specification and creating an architecture design.
          </Typography>
        </Box>
      );
    }

    if (!hasDesign) {
      return (
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', py: 8 }}>
          <Typography variant="h6" color="text.secondary" sx={{ mb: 1 }}>
            No design generated yet
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
            Generate an architecture design from your specification.
          </Typography>
          <Button
            variant="contained"
            startIcon={<Rocket size={16} />}
            onClick={handleRegenerateDesign}
          >
            Generate Design
          </Button>
        </Box>
      );
    }

    return (
      <Box>
        {/* Requirements Section */}
        <RequirementsSection requirements={design!.requirements} />

        {/* Components Section */}
        <ComponentsSection components={design!.components} />

        {/* Actions */}
        <Stack direction="row" justifyContent="flex-end" gap={1} sx={{ mt: 4 }}>
          {!designApproved && (
            <>
              <Button variant="outlined" startIcon={<Edit size={16} />} onClick={startEditSpec}>
                Edit Spec
              </Button>
              <Button
                variant="outlined"
                startIcon={<Rocket size={16} />}
                onClick={handleRegenerateDesign}
              >
                Regenerate
              </Button>
              <Button
                variant="contained"
                startIcon={<Check size={16} />}
                onClick={handleApproveDesign}
                disabled={approving}
              >
                {approving ? 'Creating Components...' : 'Approve & Create Components'}
              </Button>
            </>
          )}
          {designApproved && (
            <Button
              variant="contained"
              onClick={() => navigate(projectOverviewPath(routeOrgId, projectId!))}
            >
              View Components
            </Button>
          )}
        </Stack>
      </Box>
    );
  };

  // -- Render: Main ----------------------------------------------------------

  return (
    <PageContent>
      <Button
        variant="text"
        startIcon={<ArrowLeft size={16} />}
        onClick={() => navigate(organizationOverviewPath(routeOrgId))}
        sx={{ mb: 2 }}
      >
        Back to Projects
      </Button>

      <Stack direction="row" alignItems="center" gap={2} sx={{ mb: 1 }}>
        <Typography variant="h4" fontWeight={700}>
          {project.name}
        </Typography>
        {activeStep === 0 && !editing && spec.content && (
          <Chip
            label={specApproved ? 'Approved' : 'Draft'}
            color={specApproved ? 'primary' : 'default'}
            size="small"
          />
        )}
        {activeStep === 1 && hasDesign && (
          <Chip
            label={designApproved ? 'Approved' : 'Draft'}
            color={designApproved ? 'primary' : 'default'}
            size="small"
          />
        )}
      </Stack>

      <StepIndicator
        activeStep={activeStep}
        specApproved={specApproved}
        designApproved={designApproved ?? false}
        onStepClick={handleStepClick}
      />

      {activeStep === 0 && renderSpecStep()}
      {activeStep === 1 && renderDesignStep()}
    </PageContent>
  );
}
