import { useEffect, useState } from 'react';
import { Box, Chip, CircularProgress, IconButton, Tooltip, Typography } from '@wso2/oxygen-ui';
import type { DatabaseArtifact, DatabaseArtifactStatus, Task, TaskStatus } from '../../services/api/types';
import { restApi } from '../../services/api/rest';

const POLL_INTERVAL = 5000;

const STATUS_CONFIG: Record<DatabaseArtifactStatus, { label: string; color: string }> = {
  pending:      { label: 'PENDING',      color: 'text.disabled' },
  provisioning: { label: 'PROVISIONING', color: 'warning.main'  },
  healthy:      { label: 'HEALTHY',      color: 'success.main'  },
  faulty:       { label: 'FAULTY',       color: 'error.main'    },
};

const DB_TYPE_COLORS: Record<string, string> = {
  mysql:   '#00758f',
  mongodb: '#00ed64',
};

function DbIcon({ dbType }: { dbType?: string }) {
  const color = dbType ? (DB_TYPE_COLORS[dbType.toLowerCase()] ?? '#888') : '#888';
  const gradId = `db-grad-${dbType ?? 'default'}`;
  const label = dbType ? dbType.slice(0, 2).toUpperCase() : 'DB';
  return (
    <svg width="36" height="36" viewBox="0 0 40 40" xmlns="http://www.w3.org/2000/svg" style={{ flexShrink: 0 }}>
      <defs>
        <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.85" />
          <stop offset="100%" stopColor={color} stopOpacity="1" />
        </linearGradient>
      </defs>
      {/* Body */}
      <path d="M 7 9 A 13 3.5 0 0 0 33 9 L 33 33 A 13 3.5 0 0 1 7 33 Z" fill={`url(#${gradId})`} />
      {/* Top ellipse + highlight */}
      <ellipse cx="20" cy="9" rx="13" ry="3.5" fill={color} />
      <ellipse cx="20" cy="8.5" rx="11.5" ry="2.5" fill="#fff" opacity="0.18" />
      {/* Data disc rings */}
      <path d="M 7 17 A 13 3.5 0 0 0 33 17" fill="none" stroke="#fff" strokeWidth="0.7" opacity="0.5" />
      <path d="M 7 25 A 13 3.5 0 0 0 33 25" fill="none" stroke="#fff" strokeWidth="0.7" opacity="0.5" />
      {/* Ground shadow */}
      <ellipse cx="20" cy="36" rx="11" ry="1.2" fill="#000" opacity="0.08" />
      {/* Label */}
      <text x="20" y="23" textAnchor="middle" fontSize="6.5" fontWeight="700"
            fill="#fff" opacity="0.95" fontFamily="Inter, sans-serif" letterSpacing="0.5">
        {label}
      </text>
    </svg>
  );
}

function StatusBadge({ status }: { status: DatabaseArtifactStatus }) {
  const cfg = STATUS_CONFIG[status];
  return (
    <Typography
      variant="caption"
      sx={{
        fontWeight: 700,
        color: cfg.color,
        fontSize: '0.6rem',
        letterSpacing: '0.06em',
        flexShrink: 0,
      }}
    >
      {cfg.label}
    </Typography>
  );
}

function CopyIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
      <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
    </svg>
  );
}

function EyeIcon({ open }: { open: boolean }) {
  if (open) {
    return (
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94" />
        <path d="M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19" />
        <line x1="1" y1="1" x2="23" y2="23" />
      </svg>
    );
  }
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  );
}

function CredentialRow({ label, value, secret }: { label: string; value: string; secret?: boolean }) {
  const [visible, setVisible] = useState(false);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);

  const handleCopy = async () => {
    let ok = false;
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(value);
        ok = true;
      } else {
        // Fallback for non-secure contexts or browsers without Clipboard API.
        const ta = document.createElement('textarea');
        ta.value = value;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.focus();
        ta.select();
        ok = document.execCommand('copy');
        document.body.removeChild(ta);
      }
    } catch {
      ok = false;
    }
    if (ok) {
      setCopied(true);
      setCopyError(false);
      setTimeout(() => setCopied(false), 1500);
    } else {
      setCopied(false);
      setCopyError(true);
      setTimeout(() => setCopyError(false), 2000);
    }
  };

  const display = secret && !visible ? '••••••••••••••' : value;

  const copyTooltip = copied ? 'Copied!' : copyError ? 'Copy failed' : 'Copy';
  const copyColor = copied ? 'success.main' : copyError ? 'error.main' : 'text.secondary';

  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5, py: 0.5 }}>
      <Typography
        variant="caption"
        sx={{ color: 'text.disabled', fontSize: '0.6rem', fontWeight: 700, letterSpacing: '0.05em', width: 36, flexShrink: 0 }}
      >
        {label}
      </Typography>
      <Typography
        variant="caption"
        sx={{
          flex: 1,
          fontSize: '0.7rem',
          fontFamily: 'monospace',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
          color: 'text.primary',
        }}
      >
        {display}
      </Typography>
      {secret && (
        <Tooltip title={visible ? 'Hide' : 'Show'}>
          <IconButton size="small" onClick={() => setVisible(v => !v)} sx={{ p: 0.25, color: 'text.secondary' }}>
            <EyeIcon open={visible} />
          </IconButton>
        </Tooltip>
      )}
      <Tooltip title={copyTooltip}>
        <IconButton size="small" onClick={handleCopy} sx={{ p: 0.25, color: copyColor }}>
          <CopyIcon />
        </IconButton>
      </Tooltip>
    </Box>
  );
}


function dbTaskProgress(status: TaskStatus | undefined): number {
  if (!status || status === 'pending' || status === 'on_hold') return 0;
  if (status === 'in_progress') return 25;
  if (status === 'testing') return 80;
  if (status === 'deployed') return 100;
  return -1; // sentinel: failed — caller keeps last displayed value
}

function DatabaseCard({ db, taskStatus }: { db: DatabaseArtifact; taskStatus?: TaskStatus }) {
  const [expanded, setExpanded] = useState(false);
  const hasCredentials = db.host || db.port || db.username || db.password;

  // Show PROVISIONING as soon as the task is in_progress (25%), even before the
  // database-service registry catches up from 'pending' to 'provisioning'.
  const displayStatus: DatabaseArtifactStatus =
    taskStatus === 'in_progress' && db.status === 'pending' ? 'provisioning' : db.status;

  const [displayPct, setDisplayPct] = useState(() => {
    const p = dbTaskProgress(taskStatus);
    return p === -1 ? 0 : p;
  });
  const [barShown, setBarShown] = useState(true);
  const isTerminal = taskStatus === 'deployed' || taskStatus === 'failed';

  useEffect(() => {
    const p = dbTaskProgress(taskStatus);
    if (p !== -1) setDisplayPct(p);
  }, [taskStatus]);

  useEffect(() => {
    if (!isTerminal) return;
    const t = setTimeout(() => setBarShown(false), 2500);
    return () => clearTimeout(t);
  }, [isTerminal]);

  return (
    <Box
      sx={{
        position: 'relative',
        borderRadius: 1.25,
        border: '1px solid',
        borderColor: expanded ? 'primary.light' : 'divider',
        bgcolor: 'background.paper',
        mb: 1,
        overflow: 'hidden',
        cursor: hasCredentials ? 'pointer' : 'default',
        transition: 'border-color 0.15s',
        '&:hover': hasCredentials ? { borderColor: 'primary.light' } : {},
      }}
      onClick={() => { if (hasCredentials) setExpanded(e => !e); }}
    >
      {/* Card header */}
      <Box sx={{ display: 'flex', gap: 1.25, p: 1.25 }}>
        <DbIcon dbType={db.dbType} />
        <Box sx={{ flex: 1, minWidth: 0 }}>
          <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 0.5 }}>
            <Typography
              variant="body2"
              sx={{ fontWeight: 600, fontSize: '0.8rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
            >
              {db.dbName || db.requestedName || (db.components?.[0] ?? 'database')}
            </Typography>
            <StatusBadge status={displayStatus} />
          </Box>
          <Typography variant="caption" sx={{ color: 'text.secondary', fontSize: '0.7rem', display: 'block' }}>
            {db.dbType ? db.dbType.charAt(0).toUpperCase() + db.dbType.slice(1) : 'Database'}
            {db.components?.length ? ` · ${db.components.join(', ')}` : ''}
          </Typography>
        </Box>
      </Box>

      {/* Credentials panel */}
      {expanded && hasCredentials && (
        <Box
          sx={{ px: 1.25, pb: 1.25, pt: 0.25 }}
          onClick={e => e.stopPropagation()}
        >
          <Typography
            variant="caption"
            sx={{ color: 'text.disabled', fontSize: '0.6rem', fontWeight: 700, letterSpacing: '0.08em', display: 'block', mb: 0.5 }}
          >
            CREDENTIALS
          </Typography>
          <Box sx={{ borderTop: '1px solid', borderColor: 'divider', pt: 0.5 }}>
            {db.host     && <CredentialRow label="HOST" value={db.host} />}
            {db.port     && <CredentialRow label="PORT" value={String(db.port)} />}
            {db.username && <CredentialRow label="USER" value={db.username} />}
            {db.password && <CredentialRow label="PASS" value={db.password} secret />}
          </Box>
        </Box>
      )}

      {/* Provisioning progress bar */}
      {barShown && (
        <Box
          sx={{
            height: '3px',
            width: '100%',
            bgcolor: 'action.hover',
            overflow: 'hidden',
            opacity: isTerminal ? 0 : 1,
            transition: isTerminal ? 'opacity 0.5s ease 2s' : 'none',
          }}
        >
          <Box
            sx={{
              height: '100%',
              width: `${displayPct}%`,
              bgcolor: 'primary.main',
              transition: 'width 0.6s ease',
            }}
          />
        </Box>
      )}
    </Box>
  );
}

interface DatabaseArtifactsPanelProps {
  orgId: string;
  projectId: string;
  tasks?: Task[];
  onHasArtifacts?: (has: boolean) => void;
}

export function DatabaseArtifactsPanel({ orgId, projectId, tasks, onHasArtifacts }: DatabaseArtifactsPanelProps) {
  const [databases, setDatabases] = useState<DatabaseArtifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [apiError, setApiError] = useState<string | null>(null);

  useEffect(() => {
    if (!orgId || !projectId) return;

    let cancelled = false;

    const load = async () => {
      try {
        const data = await restApi.listDatabaseArtifacts(orgId, projectId);
        if (!cancelled) {
          setDatabases(data);
          setApiError(null);
        }
      } catch (err: any) {
        // eslint-disable-next-line no-console
        console.error('[DatabaseArtifactsPanel] failed to load:', err?.message ?? err);
        if (!cancelled) setApiError(err?.message ?? 'Failed to load');
      } finally {
        if (!cancelled) setLoading(false);
      }
    };

    load();
    const interval = setInterval(load, POLL_INTERVAL);
    return () => {
      cancelled = true;
      clearInterval(interval);
    };
  }, [orgId, projectId]);

  const healthy      = databases.filter(d => d.status === 'healthy').length;
  const provisioning = databases.filter(d => d.status === 'provisioning').length;
  const faulty       = databases.filter(d => d.status === 'faulty').length;
  const pending      = databases.filter(d => d.status === 'pending').length;

  const total = databases.length;
  const visible = !loading && !apiError && total > 0;

  useEffect(() => {
    onHasArtifacts?.(visible);
  }, [visible, onHasArtifacts]);

  return (
    <Box
      sx={{
        position: 'sticky',
        top: 64,
        height: 'calc(100vh - 64px)',
        display: 'flex',
        flexDirection: 'column',
        borderLeft: '1px solid',
        borderColor: 'divider',
        bgcolor: 'transparent',
        pl: 2,
        pr: 1.5,
        pt: 2,
        overflowX: 'hidden',
        overflowY: visible ? 'auto' : 'hidden',
        opacity: visible ? 1 : 0,
        transform: visible ? 'translateX(0)' : 'translateX(16px)',
        transition: 'opacity 0.3s ease, transform 0.35s ease',
        pointerEvents: visible ? 'auto' : 'none',
      }}
    >
      {/* Header */}
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1.5 }}>
        <Typography variant="body2" sx={{ fontWeight: 700 }}>
          Artifacts
        </Typography>
        {total > 0 && (
          <Chip
            label={total}
            size="small"
            sx={{ height: 18, fontSize: '0.65rem', fontWeight: 700, px: 0.25 }}
          />
        )}
        {loading && <CircularProgress size={12} sx={{ ml: 'auto' }} />}
      </Box>

      {/* Status summary */}
      {total > 0 && (
        <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.75, mb: 1.5 }}>
          {healthy > 0 && (
            <Typography variant="caption" sx={{ color: 'success.main', fontWeight: 600, fontSize: '0.7rem' }}>
              {healthy} healthy
            </Typography>
          )}
          {provisioning > 0 && (
            <Typography variant="caption" sx={{ color: 'warning.main', fontWeight: 600, fontSize: '0.7rem' }}>
              {provisioning} provisioning
            </Typography>
          )}
          {faulty > 0 && (
            <Typography variant="caption" sx={{ color: 'error.main', fontWeight: 600, fontSize: '0.7rem' }}>
              {faulty} faulty
            </Typography>
          )}
          {pending > 0 && (
            <Typography variant="caption" sx={{ color: 'text.disabled', fontWeight: 600, fontSize: '0.7rem' }}>
              {pending} pending
            </Typography>
          )}
        </Box>
      )}

      {/* Database cards */}
      <Box sx={{ flex: 1 }}>
        {loading && total === 0 ? (
          <Box sx={{ display: 'flex', justifyContent: 'center', pt: 4 }}>
            <CircularProgress size={20} />
          </Box>
        ) : apiError ? (
          <Typography variant="caption" sx={{ color: 'error.main', display: 'block', mt: 2, wordBreak: 'break-word' }}>
            {apiError}
          </Typography>
        ) : (
          databases.map(db => {
            const taskStatus = tasks?.find(t => t.componentTaskId === db.referenceId)?.status;
            return <DatabaseCard key={db.id} db={db} taskStatus={taskStatus} />;
          })
        )}
      </Box>
    </Box>
  );
}
