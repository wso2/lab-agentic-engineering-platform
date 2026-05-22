import { useEffect, useRef, useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { alpha, Box, Button, CircularProgress, IconButton, PageContent, Typography } from '@wso2/oxygen-ui';
import { AlertTriangle, CheckCircle, ChevronRight, Clock, Info, X } from '@wso2/oxygen-ui-icons-react';
import { useProjectBoard } from '../hooks/useProjectBoard';
import { AnimatedBanner } from '../components/tasks/AnimatedBanner';
import { DatabaseArtifactsPanel } from '../components/tasks/DatabaseArtifactsPanel';
import { TaskSection } from '../components/tasks/TaskSection';
import { TasksPageHeader } from '../components/tasks/TasksPageHeader';
import type { SectionConfig } from '../components/tasks/types';

const SECTIONS: SectionConfig[] = [
  { key: 'inProgress', label: 'In Progress', isPrimary: true,  dotColor: 'primary',      borderColor: null            },
  { key: 'todo',       label: 'To Do',       isPrimary: false, dotColor: null,           borderColor: null            },
  { key: 'done',       label: 'Done',        isPrimary: false, dotColor: null,           borderColor: null            },
  { key: 'onHold',     label: 'On Hold',     isPrimary: false, dotColor: 'warning.main', borderColor: 'warning.main'  },
  { key: 'failed',     label: 'Failed',      isPrimary: false, dotColor: 'error.main',   borderColor: 'error.main'    },
];

export default function ProjectTasksPage() {
  const { orgId, projectId } = useParams<{ orgId: string; projectId: string }>();
  const navigate = useNavigate();
  const failedRef = useRef<HTMLDivElement>(null);

  const [expandedSections, setExpandedSections] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(SECTIONS.map(s => [s.key, s.key === 'todo' || s.key === 'inProgress']))
  );
  const [hasArtifacts, setHasArtifacts] = useState(false);
  const prevCountsRef = useRef<Record<string, number>>({});

  const {
    board,
    isLoading,
    error,
    isGenerating,
    isDispatching,
    actionError,
    totalTasks,
    handleGenerate,
    handleStartImplementation,
    clearActionError,
    generateBanner,
    hideGenerateButton,
    clearGenerateBanner,
  } = useProjectBoard(orgId, projectId);

  // Auto-expand sections when their task count increases
  useEffect(() => {
    const prev = prevCountsRef.current;
    const toExpand: Record<string, boolean> = {};
    let hasNew = false;
    for (const section of SECTIONS) {
      const prevCount = prev[section.key] ?? 0;
      const currCount = board[section.key].length;
      if (currCount > prevCount) {
        toExpand[section.key] = true;
        hasNew = true;
      }
      prev[section.key] = currCount;
    }
    if (hasNew) {
      setExpandedSections(s => ({ ...s, ...toExpand }));
    }
  }, [board]);

  // Snapshot last non-null values so banners can render during their exit animation
  const lastGenerateBanner = useRef(generateBanner);
  if (generateBanner) lastGenerateBanner.current = generateBanner;
  const lastActionError = useRef(actionError);
  if (actionError) lastActionError.current = actionError;

  useEffect(() => {
    if (generateBanner?.autoDismiss) {
      const t = setTimeout(() => clearGenerateBanner(), 5000);
      return () => clearTimeout(t);
    }
    return undefined;
  }, [generateBanner, clearGenerateBanner]);

  if (isLoading) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', pt: 16, gap: 1.5 }}>
          <CircularProgress size={28} thickness={3} />
          <Typography variant="body2" color="text.disabled">Loading tasks…</Typography>
        </Box>
      </PageContent>
    );
  }

  if (error) {
    return (
      <PageContent>
        <Box sx={{ display: 'flex', justifyContent: 'center', pt: 16 }}>
          <Typography variant="body2" color="error.main">{error}</Typography>
        </Box>
      </PageContent>
    );
  }

  const allTasks = [...board.todo, ...board.inProgress, ...board.done, ...board.onHold, ...board.failed];

  const failedCount     = board.failed.length;
  const inProgressCount = board.inProgress.length;
  const todoCount       = board.todo.length;
  const onHoldCount     = board.onHold.length;
  const buildingDepCount = board.done.filter(t => t.status === 'building').length;

  type BannerVariant = 'failed' | 'in_progress' | 'on_hold_building' | 'all_done' | null;
  let bannerVariant: BannerVariant = null;
  if (totalTasks > 0) {
    if (failedCount > 0)                                               bannerVariant = 'failed';
    else if (inProgressCount > 0)                                      bannerVariant = 'in_progress';
    else if (onHoldCount > 0 && buildingDepCount > 0 && todoCount === 0) bannerVariant = 'on_hold_building';
    else if (todoCount === 0)                                          bannerVariant = 'all_done';
  }

  return (
    <PageContent fullWidth noPadding sx={{ display: 'flex', flexDirection: 'column' }}>
      <Box sx={{
        display: 'grid',
        gridTemplateColumns: hasArtifacts ? '1fr 260px' : '1fr 0px',
        alignItems: 'start',
        transition: 'grid-template-columns 0.35s ease',
      }}>

        {/* Left: tasks column */}
        <Box sx={{ display: 'flex', flexDirection: 'column', minWidth: 0, pl: 2, pr: 2, pt: 2 }}>

        <TasksPageHeader
          projectId={projectId ?? ''}
          totalTasks={totalTasks}
          isGenerating={isGenerating}
          isDispatching={isDispatching}
          githubProjectUrl={board.url}
          hideGenerateButton={hideGenerateButton}
          onGenerate={handleGenerate}
          onStartImplementation={handleStartImplementation}
        />

        {/* Generate / action banners */}
        <AnimatedBanner show={!!generateBanner}>
          {(() => {
            const banner = lastGenerateBanner.current;
            if (!banner) return null;
            const color =
              banner.variant === 'info'    ? 'info.main'    :
              banner.variant === 'warning' ? 'warning.main' :
              banner.variant === 'success' ? 'success.main' : 'error.main';
            return (
              <Box
                sx={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 1.5,
                  px: 2,
                  py: 1.5,
                  mb: 2,
                  borderRadius: 1.25,
                  bgcolor: (t) =>
                    banner.variant === 'info'    ? alpha(t.palette.info.main,    0.08) :
                    banner.variant === 'warning' ? alpha(t.palette.warning.main, 0.08) :
                    banner.variant === 'success' ? alpha(t.palette.success.main, 0.08) :
                                                   alpha(t.palette.error.main,   0.08),
                  border: '1px solid',
                  borderColor: (t) =>
                    banner.variant === 'info'    ? alpha(t.palette.info.main,    0.2) :
                    banner.variant === 'warning' ? alpha(t.palette.warning.main, 0.2) :
                    banner.variant === 'success' ? alpha(t.palette.success.main, 0.2) :
                                                   alpha(t.palette.error.main,   0.2),
                }}
              >
                <Box sx={{ flexShrink: 0, display: 'flex', color }}>
                  {banner.variant === 'info'    ? <CircularProgress size={16} sx={{ color }} /> :
                   banner.variant === 'success' ? <CheckCircle size={16} /> :
                                                  <AlertTriangle size={16} />}
                </Box>
                <Typography variant="body2" sx={{ flex: 1, color, lineHeight: 1.3 }}>
                  {banner.message}
                </Typography>
                {banner.action && (
                  <Button
                    size="small"
                    variant="outlined"
                    color={banner.variant === 'warning' ? 'warning' : 'error'}
                    onClick={() => navigate(banner.action!.path)}
                    sx={{ flexShrink: 0, whiteSpace: 'nowrap' }}
                  >
                    {banner.action.label}
                  </Button>
                )}
                <IconButton size="small" onClick={clearGenerateBanner} sx={{ p: 0.5, color }}>
                  <X size={14} />
                </IconButton>
              </Box>
            );
          })()}
        </AnimatedBanner>

        <AnimatedBanner show={!!actionError}>
          <Box
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1.5,
              px: 2,
              py: 1.5,
              mb: 2,
              borderRadius: 1.25,
              bgcolor: (t) => alpha(t.palette.error.main, 0.08),
              border: '1px solid',
              borderColor: (t) => alpha(t.palette.error.main, 0.2),
            }}
          >
            <Typography variant="body2" sx={{ flex: 1, color: 'error.main', lineHeight: 1.3 }}>
              {lastActionError.current}
            </Typography>
            <IconButton size="small" onClick={clearActionError} sx={{ p: 0.5, color: 'error.main' }}>
              <X size={14} />
            </IconButton>
          </Box>
        </AnimatedBanner>

        {/* Contextual status banners */}
        <AnimatedBanner show={bannerVariant === 'failed'}>
          <Box
            onClick={() => failedRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })}
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1.5,
              px: 2,
              py: 1.5,
              mb: 2,
              borderRadius: 1.25,
              bgcolor: (t) => alpha(t.palette.error.main, 0.08),
              border: '1px solid',
              borderColor: (t) => alpha(t.palette.error.main, 0.2),
              cursor: 'pointer',
              transition: 'all 0.15s',
              '&:hover': {
                bgcolor: (t) => alpha(t.palette.error.main, 0.12),
                borderColor: (t) => alpha(t.palette.error.main, 0.3),
              },
            }}
          >
            <Box sx={{ flexShrink: 0, display: 'flex', color: 'error.main' }}>
              <AlertTriangle size={16} />
            </Box>
            <Box sx={{ flex: 1 }}>
              <Typography variant="body2" sx={{ fontWeight: 700, color: 'error.main', lineHeight: 1.3 }}>
                {failedCount} task{failedCount !== 1 ? 's' : ''} failed
              </Typography>
              <Typography variant="caption" sx={{ color: 'error.dark', lineHeight: 1.3 }}>
                Click to review failed tasks
              </Typography>
            </Box>
            <Box sx={{ flexShrink: 0, color: 'error.main', display: 'flex' }}>
              <ChevronRight size={16} />
            </Box>
          </Box>
        </AnimatedBanner>

        <AnimatedBanner show={bannerVariant === 'in_progress'}>
          <Box
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1.5,
              px: 2,
              py: 1.5,
              mb: 2,
              borderRadius: 1.25,
              bgcolor: (t) => alpha(t.palette.info.main, 0.08),
              border: '1px solid',
              borderColor: (t) => alpha(t.palette.info.main, 0.2),
            }}
          >
            <Box sx={{ flexShrink: 0, display: 'flex' }}>
              <CircularProgress size={16} sx={{ color: 'info.main' }} />
            </Box>
            <Box sx={{ flex: 1 }}>
              <Typography variant="body2" sx={{ fontWeight: 700, color: 'info.main', lineHeight: 1.3 }}>
                {inProgressCount} task{inProgressCount !== 1 ? 's' : ''} in progress
              </Typography>
              <Typography variant="caption" sx={{ color: 'text.primary', lineHeight: 1.3 }}>
                All tasks are dispatched to the agents and they are working on it
              </Typography>
            </Box>
          </Box>
        </AnimatedBanner>

        <AnimatedBanner show={bannerVariant === 'on_hold_building'}>
          <Box
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1.5,
              px: 2,
              py: 1.5,
              mb: 2,
              borderRadius: 1.25,
              bgcolor: (t) => alpha(t.palette.warning.main, 0.08),
              border: '1px solid',
              borderColor: (t) => alpha(t.palette.warning.main, 0.2),
            }}
          >
            <Box sx={{ flexShrink: 0, display: 'flex', color: 'warning.main' }}>
              <Clock size={16} />
            </Box>
            <Box sx={{ flex: 1 }}>
              <Typography variant="body2" sx={{ fontWeight: 700, color: 'warning.main', lineHeight: 1.3 }}>
                {onHoldCount} task{onHoldCount !== 1 ? 's' : ''} on hold — awaiting deployment
              </Typography>
              <Typography variant="caption" sx={{ color: 'text.primary', lineHeight: 1.3 }}>
                {buildingDepCount} dependency component{buildingDepCount !== 1 ? 's are' : ' is'} still building. Held tasks will proceed once deployment completes.
              </Typography>
            </Box>
          </Box>
        </AnimatedBanner>

        <AnimatedBanner show={bannerVariant === 'all_done'}>
          <Box
            sx={{
              display: 'flex',
              alignItems: 'center',
              gap: 1.5,
              px: 2,
              py: 1.5,
              mb: 2,
              borderRadius: 1.25,
              bgcolor: (t) => alpha(t.palette.success.main, 0.08),
              border: '1px solid',
              borderColor: (t) => alpha(t.palette.success.main, 0.2),
            }}
          >
            <Box sx={{ flexShrink: 0, display: 'flex', color: 'success.main' }}>
              <CheckCircle size={16} />
            </Box>
            <Box sx={{ flex: 1 }}>
              <Typography variant="body2" sx={{ fontWeight: 700, color: 'success.main', lineHeight: 1.3 }}>
                All pending tasks are complete
              </Typography>
              <Typography variant="caption" sx={{ color: 'text.primary', lineHeight: 1.3 }}>
                All tasks have been completed successfully
              </Typography>
            </Box>
          </Box>
        </AnimatedBanner>

        {/* Task sections */}
        <Box>
          {totalTasks === 0 && !isGenerating && !generateBanner && (
            <Box
              sx={{
                display: 'flex',
                alignItems: 'center',
                gap: 1.5,
                px: 2,
                py: 1.5,
                mb: 2,
                borderRadius: 1.25,
                bgcolor: (t) => alpha(t.palette.info.main, 0.08),
                border: '1px solid',
                borderColor: (t) => alpha(t.palette.info.main, 0.2),
              }}
            >
              <Box sx={{ flexShrink: 0, display: 'flex', color: 'info.main' }}>
                <Info size={16} />
              </Box>
              <Box sx={{ flex: 1 }}>
                <Typography variant="body2" sx={{ fontWeight: 700, color: 'info.main', lineHeight: 1.3 }}>
                  No tasks generated yet
                </Typography>
                <Typography variant="caption" sx={{ color: 'text.primary', lineHeight: 1.3 }}>
                  Tasks haven't been generated for this project. Generate tasks to get started.
                </Typography>
              </Box>
            </Box>
          )}

          {SECTIONS.map(section => (
            <Box key={section.key} ref={section.key === 'failed' ? failedRef : undefined}>
              <TaskSection
                section={section}
                tasks={board[section.key]}
                orgId={orgId ?? ''}
                projectId={projectId ?? ''}
                expanded={expandedSections[section.key] ?? false}
                onExpandedChange={(val) => setExpandedSections(s => ({ ...s, [section.key]: val }))}
              />
            </Box>
          ))}
        </Box>

        </Box>{/* end left tasks column */}

        {/* Right: database artifacts panel */}
        <DatabaseArtifactsPanel orgId={orgId ?? ''} projectId={projectId ?? ''} tasks={allTasks} onHasArtifacts={setHasArtifacts} />

      </Box>
    </PageContent>
  );
}
