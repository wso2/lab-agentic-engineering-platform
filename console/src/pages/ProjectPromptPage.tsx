import { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  Box,
  Card,
  CardContent,
  IconButton,
  Stack,
  TextField,
  Typography,
  useTheme,
} from '@wso2/oxygen-ui';
import { Building, Check, Rocket } from '@wso2/oxygen-ui-icons-react';
import { api } from '../services/api';
import { projectRequirementsPath } from '../lib/paths';

const PROMPT_TEMPLATES = [
  {
    id: 'hello-world',
    title: 'Hello World',
    description: 'A simple starter application to get up and running quickly',
    prompt: 'Build a simple hello world web application',
    Icon: Rocket,
  },
  {
    id: 'leave-management',
    title: 'Leave Management',
    description: 'An internal WSO2 employee app for managing leave requests',
    prompt: 'Build a leave management application for a internal employee app for wso2',
    Icon: Check,
  },
  {
    id: 'company-internal',
    title: 'Company Internal Apps',
    description: 'WSO2 employee productivity suite with multiple modules',
    prompt: `I need to create an app for WSO2 employees to help with their day to day tasks at WSO2.
This should be supported for both web and mobile.
Employees should be able to login using company email.
They should be able to see what's there for food (breakfast, lunch, snacks).
They should be able to apply for leaves and so on.`,
    Icon: Building,
  },
];

export default function ProjectPromptPage() {
  const navigate = useNavigate();
  const { orgId, projectId } = useParams();
  const routeOrgId = orgId ?? 'default';
  const theme = useTheme();
  const [prompt, setPrompt] = useState('');
  const [selectedCard, setSelectedCard] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // If a spec already exists, redirect to requirements page
  useEffect(() => {
    if (!projectId) return;
    api.getSpec(routeOrgId, projectId).then((spec) => {
      if (spec) {
        navigate(projectRequirementsPath(routeOrgId, projectId), { replace: true });
      }
    });
  }, [projectId, routeOrgId, navigate]);

  const handleSubmit = () => {
    if (!projectId || !prompt.trim()) return;
    setError(null);
    navigate(projectRequirementsPath(routeOrgId, projectId), {
      state: { streamPrompt: prompt.trim() },
    });
  };

  const handleCardClick = (template: (typeof PROMPT_TEMPLATES)[number]) => {
    setPrompt(template.prompt);
    setSelectedCard(template.id);
  };

  return (
    <Box
      sx={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        minHeight: 'calc(100vh - 240px)',
        px: 2,
      }}
    >
      {/* Greeting */}
      <Typography
        variant="h3"
        fontWeight={700}
        sx={{ mb: 5, textAlign: 'center' }}
      >
        What would you like to build?
      </Typography>

      {/* Prompt input */}
      <Box sx={{ width: '100%', maxWidth: 720, mb: 5 }}>
        <Card
          variant="outlined"
          sx={{
            borderRadius: 3,
            boxShadow: 1,
            transition: 'border-color 0.2s, box-shadow 0.2s',
            '&:focus-within': {
              borderColor: theme.vars?.palette.primary.main ?? 'primary.main',
              boxShadow: `0 0 0 2px ${theme.vars?.palette.primary.main ?? '#1976d2'}20`,
            },
          }}
        >
          <CardContent sx={{ p: 2.5, '&:last-child': { pb: 2 } }}>
            <TextField
              value={prompt}
              onChange={(e) => {
                setPrompt(e.target.value);
                setSelectedCard(null);
              }}
              placeholder="Describe what you want to build..."
              multiline
              minRows={3}
              maxRows={10}
              fullWidth
              sx={{
                '& .MuiOutlinedInput-notchedOutline': { border: 'none' },
                '& .MuiInputBase-input': { fontSize: '1rem', lineHeight: 1.7 },
              }}
            />
            {error && (
              <Typography variant="body2" color="error" sx={{ mt: 1 }}>
                {error}
              </Typography>
            )}
            <Stack direction="row" justifyContent="flex-end" sx={{ mt: 1 }}>
              <IconButton
                aria-label="Submit prompt"
                onClick={handleSubmit}
                disabled={!prompt.trim()}
                sx={{
                  bgcolor: prompt.trim()
                    ? theme.vars?.palette.primary.main ?? 'primary.main'
                    : theme.vars?.palette.action?.disabledBackground ?? 'action.disabledBackground',
                  color: prompt.trim()
                    ? theme.vars?.palette.primary.contrastText ?? '#fff'
                    : theme.vars?.palette.text?.disabled ?? 'text.disabled',
                  '&:hover': {
                    bgcolor: theme.vars?.palette.primary.dark ?? 'primary.dark',
                  },
                  '&.Mui-disabled': {
                    bgcolor:
                      theme.vars?.palette.action?.disabledBackground ?? 'action.disabledBackground',
                    color: theme.vars?.palette.text?.disabled ?? 'text.disabled',
                  },
                  width: 40,
                  height: 40,
                  borderRadius: '50%',
                }}
              >
                <Rocket size={20} />
              </IconButton>
            </Stack>
          </CardContent>
        </Card>
      </Box>

      {/* Suggestion cards */}
      <Stack direction="row" spacing={2.5} flexWrap="wrap" justifyContent="center" useFlexGap>
        {PROMPT_TEMPLATES.map((template) => {
          const isSelected = selectedCard === template.id;
          return (
            <Card
              key={template.id}
              variant="outlined"
              onClick={() => handleCardClick(template)}
              sx={{
                cursor: 'pointer',
                borderRadius: 2,
                width: 220,
                transition: 'border-color 0.2s, box-shadow 0.2s, transform 0.15s',
                borderColor: isSelected
                  ? theme.vars?.palette.primary.main ?? 'primary.main'
                  : undefined,
                borderWidth: isSelected ? 2 : 1,
                boxShadow: isSelected ? 2 : 0,
                '&:hover': {
                  borderColor: theme.vars?.palette.primary.main ?? 'primary.main',
                  boxShadow: 2,
                  transform: 'translateY(-2px)',
                },
              }}
            >
              <CardContent sx={{ p: 2.5, '&:last-child': { pb: 2.5 } }}>
                <template.Icon
                  size={24}
                  style={{
                    marginBottom: 12,
                    color: isSelected
                      ? (theme.vars?.palette.primary.main as string) ?? undefined
                      : (theme.vars?.palette.text?.secondary as string) ?? undefined,
                  }}
                />
                <Typography variant="subtitle1" fontWeight={600} sx={{ mb: 0.5 }}>
                  {template.title}
                </Typography>
                <Typography variant="body2" color="text.secondary" sx={{ lineHeight: 1.5 }}>
                  {template.description}
                </Typography>
              </CardContent>
            </Card>
          );
        })}
      </Stack>
    </Box>
  );
}
