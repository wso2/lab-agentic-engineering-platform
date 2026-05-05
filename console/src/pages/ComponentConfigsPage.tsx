import { useCallback, useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import {
  Box,
  Button,
  Card,
  CardContent,
  IconButton,
  Skeleton,
  Stack,
  TextField,
  Typography,
} from '@wso2/oxygen-ui';
import { Plus, Save, Settings, Trash2 } from '@wso2/oxygen-ui-icons-react';
import { api } from '../services/api';
import type { ComponentDefinition, EnvVar } from '../services/api';

export default function ComponentConfigsPage() {
  const { orgId, projectId, componentId } = useParams();
  const routeOrgId = orgId ?? 'demo-org';

  const [component, setComponent] = useState<ComponentDefinition | undefined>();
  const [envVars, setEnvVars] = useState<EnvVar[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);

  const loadData = useCallback(async () => {
    if (!projectId || !componentId) return;
    const [c, config] = await Promise.all([
      api.getComponent(routeOrgId, projectId, componentId),
      api.getComponentConfig(routeOrgId, projectId, componentId),
    ]);
    setComponent(c);
    setEnvVars(config?.envVars ?? []);
    setLoading(false);
  }, [projectId, componentId, routeOrgId]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  const handleAdd = () => {
    setEnvVars((prev) => [...prev, { key: '', value: '' }]);
    setDirty(true);
  };

  const handleRemove = (index: number) => {
    setEnvVars((prev) => prev.filter((_, i) => i !== index));
    setDirty(true);
  };

  const handleChange = (index: number, field: 'key' | 'value', val: string) => {
    setEnvVars((prev) => prev.map((ev, i) => (i === index ? { ...ev, [field]: val } : ev)));
    setDirty(true);
  };

  const handleSave = async () => {
    if (!projectId || !componentId) return;
    setSaving(true);
    // Filter out rows with empty keys
    const filtered = envVars.filter((ev) => ev.key.trim() !== '');
    const result = await api.updateComponentConfig(routeOrgId, projectId, componentId, filtered);
    if (result) {
      setEnvVars(result.envVars ?? []);
    }
    setDirty(false);
    setSaving(false);
  };

  if (loading) {
    return (
      <Box>
        <Skeleton variant="text" width="40%" height={40} />
        <Skeleton variant="rectangular" width="100%" height={200} sx={{ mt: 3, borderRadius: 1 }} />
      </Box>
    );
  }

  if (!component) {
    return (
      <Box>
        <Typography variant="h5" color="error">
          Component not found
        </Typography>
      </Box>
    );
  }

  return (
    <Box>
      <Stack direction="row" justifyContent="space-between" alignItems="center" sx={{ mb: 3 }}>
        <Box>
          <Typography variant="h4" sx={{ fontWeight: 700, mb: 0.5 }}>
            {component.name}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Environment Variables
          </Typography>
        </Box>
        <Stack direction="row" gap={1}>
          <Button
            variant="outlined"
            size="small"
            startIcon={<Plus />}
            onClick={handleAdd}
          >
            Add Variable
          </Button>
          <Button
            variant="contained"
            size="small"
            startIcon={<Save />}
            onClick={handleSave}
            disabled={saving || !dirty}
          >
            {saving ? 'Saving...' : 'Save'}
          </Button>
        </Stack>
      </Stack>

      {envVars.length === 0 ? (
        <Card variant="outlined">
          <CardContent>
            <Stack alignItems="center" gap={2} sx={{ py: 6 }}>
              <Settings size={48} opacity={0.4} />
              <Typography variant="h6" color="text.secondary">
                No environment variables configured
              </Typography>
              <Typography variant="body2" color="text.secondary">
                Add environment variables that will be injected into the component
                when it is deployed.
              </Typography>
              <Button
                variant="outlined"
                startIcon={<Plus />}
                onClick={handleAdd}
              >
                Add Variable
              </Button>
            </Stack>
          </CardContent>
        </Card>
      ) : (
        <Card variant="outlined">
          <CardContent>
            {/* Header row */}
            <Stack direction="row" gap={2} sx={{ mb: 1.5 }}>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ fontWeight: 600, flex: 1 }}
              >
                KEY
              </Typography>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ fontWeight: 600, flex: 1 }}
              >
                VALUE
              </Typography>
              <Box sx={{ width: 40 }} />
            </Stack>

            <Stack gap={1.5}>
              {envVars.map((ev, index) => (
                <Stack key={index} direction="row" gap={2} alignItems="center">
                  <TextField
                    size="small"
                    placeholder="e.g. DATABASE_URL"
                    value={ev.key}
                    onChange={(e) => handleChange(index, 'key', e.target.value)}
                    sx={{ flex: 1 }}
                    inputProps={{ style: { fontFamily: 'monospace' } }}
                  />
                  <TextField
                    size="small"
                    placeholder="value"
                    value={ev.value}
                    onChange={(e) => handleChange(index, 'value', e.target.value)}
                    sx={{ flex: 1 }}
                    inputProps={{ style: { fontFamily: 'monospace' } }}
                  />
                  <IconButton
                    size="small"
                    color="error"
                    onClick={() => handleRemove(index)}
                  >
                    <Trash2 size={16} />
                  </IconButton>
                </Stack>
              ))}
            </Stack>
          </CardContent>
        </Card>
      )}
    </Box>
  );
}
