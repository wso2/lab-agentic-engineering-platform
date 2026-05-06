import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Box, Button, Card, CardContent, Chip, Dialog, DialogActions, DialogContent,
  DialogContentText, DialogTitle, Grid, IconButton, PageContent, Skeleton, Stack, Typography,
} from '@wso2/oxygen-ui';
import { Plus, Trash2 } from '@wso2/oxygen-ui-icons-react';
import { api } from '../services/api';
import type { Project } from '../services/api';
import { projectCreatePath, projectOverviewPath } from '../lib/paths';

const phaseColors: Record<string, 'default' | 'primary' | 'success' | 'warning' | 'info'> = {
  spec: 'default',
  design: 'info',
  components: 'primary',
  implementing: 'warning',
  done: 'success',
};

const phaseLabels: Record<string, string> = {
  spec: 'Specification',
  design: 'Design',
  components: 'Components',
  implementing: 'Implementing',
  done: 'Done',
};

export default function OrgOverviewPage() {
  const navigate = useNavigate();
  const { orgId } = useParams();
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [deleteTarget, setDeleteTarget] = useState<Project | null>(null);
  const [deleting, setDeleting] = useState(false);
  const routeOrgId = orgId ?? 'demo-org';

  const loadProjects = () => {
    setLoading(true);
    api.listProjects(routeOrgId).then((data) => {
      setProjects(data);
      setLoading(false);
    }).catch(() => {
      setProjects([]);
      setLoading(false);
    });
  };

  useEffect(() => {
    loadProjects();
  }, [routeOrgId]);

  const handleDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.deleteProject(routeOrgId, deleteTarget.id);
      // Optimistically remove from local state — OC API has eventual consistency
      // so re-fetching immediately would still return the deleted project.
      setProjects((prev) => prev.filter((p) => p.id !== deleteTarget.id));
      setDeleteTarget(null);
    } catch {
      // Error is already logged by fetchJSON
    } finally {
      setDeleting(false);
    }
  };

  return (
    <PageContent>
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 3 }}>
        <Typography variant="h4" fontWeight={700}>
          Projects
        </Typography>
        <Button
          variant="contained"
          startIcon={<Plus size={16} />}
          onClick={() => navigate(projectCreatePath(routeOrgId))}
        >
          New Project
        </Button>
      </Stack>

      {loading ? (
        <Grid container spacing={2}>
          {[1, 2, 3].map((i) => (
            <Grid key={i} size={{ xs: 12, md: 4 }}>
              <Card>
                <CardContent>
                  <Skeleton variant="text" width="60%" height={28} />
                  <Skeleton variant="text" width="100%" height={20} sx={{ mt: 1 }} />
                  <Skeleton variant="text" width="40%" height={24} sx={{ mt: 2 }} />
                </CardContent>
              </Card>
            </Grid>
          ))}
        </Grid>
      ) : projects.length === 0 ? (
        <Box
          sx={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            justifyContent: 'center',
            py: 10,
          }}
        >
          <Typography variant="h6" color="text.secondary" sx={{ mb: 1 }}>
            No projects yet
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
            Create your first project to get started with the App Factory workflow.
          </Typography>
          <Button
            variant="contained"
            startIcon={<Plus size={16} />}
            onClick={() => navigate(projectCreatePath(routeOrgId))}
          >
            Create Project
          </Button>
        </Box>
      ) : (
        <Grid container spacing={2}>
          {projects.map((project) => (
            <Grid key={project.id} size={{ xs: 12, md: 4 }}>
              <Card
                sx={{
                  cursor: 'pointer',
                  transition: 'box-shadow 0.2s',
                  '&:hover': { boxShadow: 4 },
                  height: '100%',
                }}
                onClick={() => navigate(projectOverviewPath(routeOrgId, project.id))}
              >
                <CardContent>
                  <Stack direction="row" justifyContent="space-between" alignItems="flex-start" sx={{ mb: 1 }}>
                    <Typography variant="h6" fontWeight={600}>
                      {project.name}
                    </Typography>
                    <Stack direction="row" spacing={0.5} alignItems="center">
                      <Chip
                        label={phaseLabels[project.phase] ?? project.phase}
                        color={phaseColors[project.phase] ?? 'default'}
                        size="small"
                      />
                      <IconButton
                        size="small"
                        onClick={(e) => {
                          e.stopPropagation();
                          setDeleteTarget(project);
                        }}
                        sx={{
                          opacity: 0.5,
                          '&:hover': { opacity: 1, color: 'error.main' },
                        }}
                      >
                        <Trash2 size={16} />
                      </IconButton>
                    </Stack>
                  </Stack>
                  <Typography
                    variant="body2"
                    color="text.secondary"
                    sx={{
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      display: '-webkit-box',
                      WebkitLineClamp: 2,
                      WebkitBoxOrient: 'vertical',
                    }}
                  >
                    {project.prompt || 'No description yet'}
                  </Typography>
                  <Typography variant="caption" color="text.disabled" sx={{ mt: 2, display: 'block' }}>
                    Created {new Date(project.createdAt).toLocaleDateString()}
                  </Typography>
                </CardContent>
              </Card>
            </Grid>
          ))}
        </Grid>
      )}

      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)}>
        <DialogTitle>Delete Project</DialogTitle>
        <DialogContent>
          <DialogContentText>
            Are you sure you want to delete <strong>{deleteTarget?.name}</strong>? This will permanently
            remove the project and all its associated data including specifications, designs, and components.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)} disabled={deleting}>
            Cancel
          </Button>
          <Button onClick={handleDelete} color="error" variant="contained" disabled={deleting}>
            {deleting ? 'Deleting...' : 'Delete'}
          </Button>
        </DialogActions>
      </Dialog>
    </PageContent>
  );
}
