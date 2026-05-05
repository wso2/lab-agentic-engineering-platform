import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Alert, Box, Button, Stack, TextField, Typography } from '@wso2/oxygen-ui';
import { ArrowLeft } from '@wso2/oxygen-ui-icons-react';
import { api, ApiError } from '../services/api';
import { organizationOverviewPath } from '../lib/paths';

function slugify(input: string): string {
  return input
    .toLowerCase()
    .replace(/[\s_]+/g, '-')
    .replace(/[^a-z0-9-]/g, '')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '');
}

export default function OrganizationCreatePage() {
  const navigate = useNavigate();

  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<{ displayName?: string; form?: string }>({});

  const handle = slugify(displayName);

  const validate = (): boolean => {
    const next: { displayName?: string; form?: string } = {};
    if (!displayName.trim()) {
      next.displayName = 'Organization name is required';
    } else if (!handle) {
      next.displayName = 'Name must contain at least one letter or digit';
    }
    setErrors(next);
    return Object.keys(next).length === 0;
  };

  const clearError = (field: 'displayName' | 'form') => {
    setErrors((prev) => ({ ...prev, [field]: undefined }));
  };

  const handleSubmit = async () => {
    if (!validate()) return;
    setSubmitting(true);
    try {
      const org = await api.createOrganization({
        displayName: displayName.trim(),
        description: description.trim(),
      });
      navigate(organizationOverviewPath(org.name));
    } catch (err) {
      const message = err instanceof ApiError ? err.message : 'Failed to create organization. Please try again.';
      setErrors({ form: message });
      setSubmitting(false);
    }
  };

  return (
    <Box sx={{ maxWidth: 640 }}>
      <Button
        variant="text"
        startIcon={<ArrowLeft size={16} />}
        onClick={() => navigate(-1)}
        sx={{ mb: 2 }}
      >
        Back
      </Button>

      <Typography variant="h4" fontWeight={700} sx={{ mb: 1 }}>
        Create Organization
      </Typography>
      <Typography variant="body1" color="text.secondary" sx={{ mb: 4 }}>
        Create a new organization. We'll provision a corresponding namespace in OpenChoreo so projects, components, and credentials stay isolated per org.
      </Typography>

      <Stack spacing={3}>
        {errors.form && (
          <Alert severity="error" onClose={() => clearError('form')}>
            {errors.form}
          </Alert>
        )}
        <TextField
          label="Organization Name"
          placeholder="e.g. Platform Team"
          value={displayName}
          onChange={(e) => {
            setDisplayName(e.target.value);
            if (errors.displayName) clearError('displayName');
          }}
          error={Boolean(errors.displayName)}
          helperText={errors.displayName ?? (handle ? `Handle: ${handle}` : 'Lowercase, alphanumeric and hyphens')}
          required
          fullWidth
        />
        <TextField
          label="Description (optional)"
          placeholder="What is this organization for?"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          fullWidth
          multiline
          minRows={2}
        />

        <Stack direction="row" justifyContent="flex-end" gap={1}>
          <Button variant="outlined" onClick={() => navigate(-1)}>
            Cancel
          </Button>
          <Button variant="contained" onClick={handleSubmit} disabled={submitting}>
            {submitting ? 'Creating...' : 'Create Organization'}
          </Button>
        </Stack>
      </Stack>
    </Box>
  );
}
