import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Alert, Box, Button, PageContent, Stack, TextField, Typography } from '@wso2/oxygen-ui';
import { ArrowLeft } from '@wso2/oxygen-ui-icons-react';
import { api, ApiError } from '../services/api';
import { organizationOverviewPath, projectOverviewPath } from '../lib/paths';

export default function ProjectCreatePage() {
  const navigate = useNavigate();
  const { orgId } = useParams();
  const routeOrgId = orgId ?? 'demo-org';

  const [name, setName] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<{ name?: string; form?: string }>({});

  const validate = (): boolean => {
    const nextErrors: { name?: string; form?: string } = {};
    if (!name.trim()) {
      nextErrors.name = 'Project name is required';
    }
    setErrors(nextErrors);
    return Object.keys(nextErrors).length === 0;
  };

  const clearError = (field: 'name' | 'form') => {
    setErrors((prev) => ({ ...prev, [field]: undefined }));
  };

  const handleSubmit = async () => {
    if (!validate()) return;

    setSubmitting(true);
    try {
      const project = await api.createProject(routeOrgId, {
        name: name.trim(),
      });
      navigate(projectOverviewPath(routeOrgId, project.id));
    } catch (err) {
      const message = err instanceof ApiError ? err.message : 'Failed to create project. Please try again.';
      setErrors({ form: message });
      setSubmitting(false);
    }
  };

  return (
    <PageContent>
    <Box sx={{ maxWidth: 640 }}>
      <Button
        variant="text"
        startIcon={<ArrowLeft size={16} />}
        onClick={() => navigate(organizationOverviewPath(routeOrgId))}
        sx={{ mb: 2 }}
      >
        Back to Projects
      </Button>

      <Typography variant="h4" fontWeight={700} sx={{ mb: 1 }}>
        Create Project
      </Typography>
      <Typography variant="body1" color="text.secondary" sx={{ mb: 4 }}>
        Name your project — we'll provision a Git repository for you.
      </Typography>

      <Stack spacing={3}>
        {errors.form && (
          <Alert severity="error" onClose={() => clearError('form')}>
            {errors.form}
          </Alert>
        )}
        <TextField
          label="Project Name"
          placeholder="e.g. Hello World Service"
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            if (errors.name) clearError('name');
          }}
          error={Boolean(errors.name)}
          helperText={errors.name}
          required
          fullWidth
          autoFocus
        />

        <Stack direction="row" justifyContent="flex-end" gap={1}>
          <Button
            variant="outlined"
            onClick={() => navigate(organizationOverviewPath(routeOrgId))}
          >
            Cancel
          </Button>
          <Button
            variant="contained"
            onClick={handleSubmit}
            disabled={submitting}
          >
            {submitting ? 'Creating...' : 'Create Project'}
          </Button>
        </Stack>
      </Stack>
    </Box>
    </PageContent>
  );
}
