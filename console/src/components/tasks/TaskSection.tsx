import { Accordion, AccordionDetails, AccordionSummary, Box, Typography, useTheme } from '@wso2/oxygen-ui';
import { ChevronDown } from '@wso2/oxygen-ui-icons-react';
import { TaskRow } from './TaskRow';
import type { Task } from '../../services/api';
import type { SectionConfig } from './types';

interface TaskSectionProps {
  section: SectionConfig;
  tasks: Task[];
  orgId: string;
  projectId: string;
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
}

export function TaskSection({ section, tasks, orgId, projectId, expanded, onExpandedChange }: TaskSectionProps) {
  const theme = useTheme();

  const labelColor = section.isPrimary ? theme.palette.primary.main : theme.palette.text.secondary;

  return (
    <Accordion
      expanded={expanded}
      onChange={(_, val) => onExpandedChange(val)}
      disableGutters
      elevation={0}
      sx={{
        mb: 0.75,
        border: '1px solid',
        borderColor: 'divider',
        borderRadius: 1.25,
        '&:before': { display: 'none' },
        overflow: 'hidden',
      }}
    >
      <AccordionSummary
        expandIcon={<ChevronDown size={14} style={{ color: labelColor }} />}
        sx={{
          minHeight: 40,
          px: 1.75,
          py: 0,
          '& .MuiAccordionSummary-content': { my: 1, alignItems: 'center', gap: 0.75 },
        }}
      >
        <Typography sx={{ fontSize: '0.72rem', fontWeight: 700, color: labelColor, textTransform: 'uppercase', letterSpacing: '0.06em' }}>
          {section.label}
        </Typography>
        <Typography sx={{ fontSize: '0.72rem', fontWeight: 500, color: labelColor, opacity: 0.7 }}>
          {tasks.length}
        </Typography>
      </AccordionSummary>

      <AccordionDetails sx={{ p: 1.25, display: 'flex', flexDirection: 'column', gap: 0.75 }}>
        {tasks.length > 0 ? (
          tasks.map((task, i) => (
            <TaskRow
              key={task.id}
              task={task}
              section={section}
              orgId={orgId}
              projectId={projectId}
              index={i}
            />
          ))
        ) : (
          <Box sx={{ py: 2, display: 'flex', justifyContent: 'center' }}>
            <Typography variant="body2" color="text.disabled">No tasks</Typography>
          </Box>
        )}
      </AccordionDetails>
    </Accordion>
  );
}
