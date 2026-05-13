import { useMemo, useState } from 'react';
import { useTheme, alpha } from '@mui/material/styles';
import type { Theme } from '@mui/material/styles';
import { StageDrawer } from './StageDrawer.js';
import type {
  ProjectStatusPolylineProps,
  Stage,
  ProjectState,
  FocusOverride,
} from './types.js';

const NODE_SIZE = 56;
const RING_ANIM = `
@keyframes asdlc-ps-ring {
  0%   { transform: scale(0.9); opacity: 0.55; }
  100% { transform: scale(1.35); opacity: 0; }
}
@keyframes asdlc-ps-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50%      { opacity: 0.5; transform: scale(1.25); }
}
`;

// Derived per-stage status used internally. The public Stage.state only
// distinguishes done / active / pending / blocked; we promote the first
// pending stage after the last-done/active node to "next" so it can wear
// the distinct dashed-halo treatment.
type DerivedStatus = 'done' | 'active' | 'next' | 'queued' | 'blocked';

function deriveStatuses(stages: Stage[]): DerivedStatus[] {
  let progressed = -1; // index of the last done or active stage
  stages.forEach((s, i) => {
    if (s.state === 'done' || s.state === 'active') progressed = i;
  });
  let nextAssigned = false;
  return stages.map((s, i) => {
    if (s.state === 'done') return 'done';
    if (s.state === 'active') return 'active';
    if (s.state === 'blocked') return 'blocked';
    // pending
    if (!nextAssigned && i === progressed + 1) {
      nextAssigned = true;
      return 'next';
    }
    return 'queued';
  });
}

// Progress fill ends at the centre of the last "done" or "active" node.
function computeProgress(statuses: DerivedStatus[]): number {
  const n = statuses.length;
  if (n <= 1) return 0;
  let last = -1;
  statuses.forEach((s, i) => {
    if (s === 'done' || s === 'active') last = i;
  });
  if (last <= 0) return 0;
  return last / (n - 1);
}

// Cursor sits 35% of the way from the last-done node towards the next stage.
// Hidden when something is actively running (the pulsing puck carries that
// meaning), at the very start (nothing done yet), or at the very end.
function computeCursor(statuses: DerivedStatus[]): number | null {
  const n = statuses.length;
  if (n <= 1) return null;
  let lastDone = -1;
  let hasActive = false;
  statuses.forEach((s, i) => {
    if (s === 'done') lastDone = i;
    if (s === 'active') hasActive = true;
  });
  if (hasActive) return null;
  if (lastDone < 0) return null;
  if (lastDone >= n - 1) return null;
  return (lastDone + 0.35) / (n - 1);
}

function deriveProjectState(stages: Stage[]): ProjectState {
  if (stages.length === 0) return 'paused';
  if (stages.some((s) => s.state === 'active')) return 'active';
  if (stages.some((s) => s.state === 'blocked')) return 'blocked';
  if (stages.every((s) => s.state === 'done')) return 'done';
  return 'paused';
}

function deriveFocus(stages: Stage[], statuses: DerivedStatus[]): FocusOverride {
  const activeIdx = statuses.indexOf('active');
  if (activeIdx >= 0) {
    const s = stages[activeIdx];
    return {
      eyebrowKey: 'currently',
      eyebrow: 'Currently',
      stage: s.name,
      detail: s.help || s.headline,
    };
  }
  const blockedIdx = statuses.indexOf('blocked');
  if (blockedIdx >= 0) {
    const s = stages[blockedIdx];
    return {
      eyebrowKey: 'blocked',
      eyebrow: 'Blocked',
      stage: s.name,
      detail: s.help || s.headline,
    };
  }
  const nextIdx = statuses.indexOf('next');
  if (nextIdx >= 0) {
    const s = stages[nextIdx];
    return {
      eyebrowKey: 'next',
      eyebrow: 'Up next',
      stage: s.name,
      detail: s.help || s.headline,
    };
  }
  if (stages.length && stages.every((s) => s.state === 'done')) {
    const last = stages[stages.length - 1];
    return {
      eyebrowKey: 'shipped',
      eyebrow: 'Shipped',
      detail: last.help || 'All stages complete',
    };
  }
  return { eyebrowKey: 'idle', eyebrow: 'Idle', detail: 'No active stage' };
}

function subLabel(status: DerivedStatus): string {
  switch (status) {
    case 'done':
      return 'done';
    case 'active':
      return 'live';
    case 'next':
      return 'next';
    case 'blocked':
      return 'stuck';
    case 'queued':
    default:
      return '—';
  }
}

interface PuckColors {
  bg: string;
  border: string;
  fg: string;
  /** Active state gets an outer halo via box-shadow. */
  glow?: string;
}

function puckColors(theme: Theme, status: DerivedStatus, isDark: boolean): PuckColors {
  const surface = theme.palette.background.paper;
  const line = theme.palette.divider;
  const subtle = theme.palette.text.disabled;
  switch (status) {
    case 'done':
      return {
        bg: alpha(theme.palette.success.main, isDark ? 0.18 : 0.1),
        border: theme.palette.success.main,
        fg: isDark ? theme.palette.success.light : theme.palette.success.dark,
      };
    case 'active':
      return {
        bg: alpha(theme.palette.primary.main, isDark ? 0.2 : 0.12),
        border: theme.palette.primary.main,
        fg: isDark ? theme.palette.primary.light : theme.palette.primary.dark,
        glow: alpha(theme.palette.primary.main, 0.28),
      };
    case 'next':
      return {
        bg: surface,
        border: theme.palette.primary.main,
        fg: isDark ? theme.palette.primary.light : theme.palette.primary.dark,
      };
    case 'blocked':
      return {
        bg: alpha(theme.palette.error.main, isDark ? 0.18 : 0.12),
        border: theme.palette.error.main,
        fg: isDark ? theme.palette.error.light : theme.palette.error.dark,
      };
    case 'queued':
    default:
      return { bg: surface, border: line, fg: subtle };
  }
}

function StatePill({
  state,
  theme,
  isDark,
}: {
  state: ProjectState;
  theme: Theme;
  isDark: boolean;
}) {
  const monoFamily = `'Geist Mono', ui-monospace, 'SF Mono', Menlo, monospace`;
  let bg = alpha(theme.palette.text.primary, isDark ? 0.06 : 0.04);
  let fg = theme.palette.text.secondary;
  let label = 'Paused';
  let dotPulse = false;
  switch (state) {
    case 'active':
      bg = alpha(theme.palette.primary.main, isDark ? 0.22 : 0.14);
      fg = isDark ? theme.palette.primary.light : theme.palette.primary.dark;
      label = 'In progress';
      dotPulse = true;
      break;
    case 'done':
      bg = alpha(theme.palette.success.main, isDark ? 0.2 : 0.12);
      fg = isDark ? theme.palette.success.light : theme.palette.success.dark;
      label = 'Shipped';
      break;
    case 'blocked':
      bg = alpha(theme.palette.error.main, isDark ? 0.2 : 0.14);
      fg = isDark ? theme.palette.error.light : theme.palette.error.dark;
      label = 'Blocked';
      break;
    default:
      label = 'Paused';
  }
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        height: 24,
        padding: '0 10px',
        borderRadius: 999,
        font: `500 11.5px/1 ${monoFamily}`,
        letterSpacing: '0.02em',
        background: bg,
        color: fg,
        border: state === 'paused' ? `1px solid ${theme.palette.divider}` : '1px solid transparent',
      }}
    >
      <span
        style={{
          width: 6,
          height: 6,
          borderRadius: 999,
          background: 'currentColor',
          opacity: dotPulse ? 1 : 0.6,
          animation: dotPulse ? 'asdlc-ps-pulse 1.6s cubic-bezier(.2,.7,.2,1) infinite' : undefined,
        }}
      />
      {label}
    </span>
  );
}

export function ProjectStatusPolyline({
  stages,
  onStageClick,
  interactive = true,
  mode,
  showHeader = true,
  projectState,
  focus,
}: ProjectStatusPolylineProps) {
  const theme = useTheme();
  const effectiveMode = mode ?? theme.palette.mode;
  const isDark = effectiveMode === 'dark';
  const [picked, setPicked] = useState<Stage | null>(null);

  const statuses = useMemo(() => deriveStatuses(stages), [stages]);
  const progress = useMemo(() => computeProgress(statuses), [statuses]);
  const cursor = useMemo(() => computeCursor(statuses), [statuses]);
  const derivedProjectState = useMemo(() => deriveProjectState(stages), [stages]);
  const effectiveState = projectState ?? derivedProjectState;
  const effectiveFocus = focus ?? deriveFocus(stages, statuses);

  const cols = stages.length || 1;

  const surface = theme.palette.background.paper;
  const text = theme.palette.text.primary;
  const muted = theme.palette.text.secondary;
  const subtle = theme.palette.text.disabled;
  const line = theme.palette.divider;
  const lineSoft = alpha(theme.palette.text.primary, isDark ? 0.06 : 0.08);
  const accent = theme.palette.primary.main;
  const done = theme.palette.success.main;
  const sunk = alpha(theme.palette.text.primary, isDark ? 0.08 : 0.05);
  const fontFamily = theme.typography.fontFamily;
  const monoFamily = `'Geist Mono', ui-monospace, 'SF Mono', Menlo, monospace`;

  const railTop = NODE_SIZE / 2 - 1;
  const colHalf = `calc(100% / ${cols} / 2)`;
  const doneCount = stages.filter((s) => s.state === 'done').length;
  const pct = cols ? Math.round((doneCount / cols) * 100) : 0;

  const handlePick = (stage: Stage) => {
    if (!interactive) return;
    if (onStageClick) {
      onStageClick(stage);
      return;
    }
    setPicked(stage);
  };

  const eyebrowKey = effectiveFocus.eyebrowKey ?? 'idle';
  let eyebrowColor = subtle;
  let eyebrowDotBg = subtle;
  let eyebrowDotPulse = false;
  if (eyebrowKey === 'currently') {
    eyebrowColor = isDark ? theme.palette.primary.light : theme.palette.primary.dark;
    eyebrowDotBg = accent;
    eyebrowDotPulse = true;
  } else if (eyebrowKey === 'next') {
    eyebrowColor = isDark ? theme.palette.primary.light : theme.palette.primary.dark;
    eyebrowDotBg = accent;
  } else if (eyebrowKey === 'shipped') {
    eyebrowColor = isDark ? theme.palette.success.light : theme.palette.success.dark;
    eyebrowDotBg = done;
  } else if (eyebrowKey === 'blocked') {
    eyebrowColor = isDark ? theme.palette.error.light : theme.palette.error.dark;
    eyebrowDotBg = theme.palette.error.main;
  }

  return (
    <div
      style={{
        background: surface,
        color: text,
        fontFamily,
        padding: '36px 40px 40px',
        borderRadius: 14,
        border: `1px solid ${line}`,
        boxShadow: '0 1px 2px rgba(0,0,0,0.02)',
        boxSizing: 'border-box',
      }}
    >
      <style>{RING_ANIM}</style>

      {showHeader && (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 24,
            paddingBottom: 22,
            marginBottom: 28,
            borderBottom: `1px solid ${lineSoft}`,
          }}
        >
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
            <span
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: 8,
                font: `500 10.5px/1 ${monoFamily}`,
                letterSpacing: '0.08em',
                textTransform: 'uppercase',
                color: eyebrowColor,
              }}
            >
              <span
                style={{
                  display: 'inline-block',
                  width: 4,
                  height: 4,
                  borderRadius: 999,
                  background: eyebrowDotBg,
                  animation: eyebrowDotPulse
                    ? 'asdlc-ps-pulse 1.6s cubic-bezier(.2,.7,.2,1) infinite'
                    : undefined,
                }}
              />
              {effectiveFocus.eyebrow}
            </span>
            <div
              style={{
                display: 'flex',
                alignItems: 'baseline',
                gap: 10,
                font: `500 17px/1.2 ${fontFamily}`,
                letterSpacing: '-0.02em',
                color: text,
                flexWrap: 'wrap',
              }}
            >
              {effectiveFocus.stage && (
                <span style={{ color: text }}>{effectiveFocus.stage}</span>
              )}
              {effectiveFocus.stage && effectiveFocus.detail && (
                <span style={{ color: subtle, fontWeight: 400 }}>·</span>
              )}
              {effectiveFocus.detail && (
                <span
                  style={{
                    font: `400 14px/1.2 ${fontFamily}`,
                    letterSpacing: '-0.01em',
                    color: muted,
                  }}
                >
                  {effectiveFocus.detail}
                </span>
              )}
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 14, flexShrink: 0 }}>
            <StatePill state={effectiveState} theme={theme} isDark={isDark} />
            <div
              style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'flex-end',
                gap: 6,
                minWidth: 160,
              }}
            >
              <span
                style={{
                  font: `500 11px/1 ${monoFamily}`,
                  letterSpacing: '0.04em',
                  textTransform: 'uppercase',
                  color: subtle,
                }}
              >
                {doneCount} / {cols} stages · {pct}%
              </span>
              <div
                style={{
                  width: 160,
                  height: 4,
                  background: sunk,
                  borderRadius: 999,
                  overflow: 'hidden',
                }}
              >
                <span
                  style={{
                    display: 'block',
                    height: '100%',
                    width: `${pct}%`,
                    background: `linear-gradient(90deg, ${done} 0%, ${done} 65%, ${accent} 100%)`,
                    borderRadius: 'inherit',
                    transition: 'width .45s cubic-bezier(.2,.7,.2,1)',
                  }}
                />
              </div>
            </div>
          </div>
        </div>
      )}

      <div style={{ position: 'relative', paddingTop: 4 }}>
        {/* Base rail — dashed pattern to read as "future". */}
        <div
          style={{
            position: 'absolute',
            top: railTop,
            height: 2,
            left: colHalf,
            right: colHalf,
            borderRadius: 1,
            background: `linear-gradient(90deg, ${line} 50%, transparent 0) 0 0 / 8px 2px repeat-x`,
          }}
        />
        {/* Progress fill — solid, green→indigo at the tip. */}
        <div
          style={{
            position: 'absolute',
            top: railTop,
            height: 2,
            left: colHalf,
            width: `calc((100% - 100% / ${cols}) * ${progress})`,
            background: `linear-gradient(90deg, ${done} 0%, ${done} 80%, ${accent} 100%)`,
            borderRadius: 1,
            transition: 'width .45s cubic-bezier(.2,.7,.2,1)',
          }}
        />

        {/* "You are here" cursor — only when nothing is live and some progress exists. */}
        {cursor != null && (
          <div
            aria-hidden
            style={{
              position: 'absolute',
              top: railTop + 1,
              transform: 'translate(-50%, -50%)',
              left: `calc((100% / ${cols}) / 2 + (100% - 100% / ${cols}) * ${cursor})`,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              width: 18,
              height: 18,
              borderRadius: 999,
              background: surface,
              border: `1.5px solid ${accent}`,
              color: accent,
              transition: 'left .45s cubic-bezier(.2,.7,.2,1)',
              zIndex: 1,
            }}
          >
            <svg
              width="8"
              height="8"
              viewBox="0 0 8 8"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.6"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M2.5 1.5L5.5 4l-3 2.5" />
            </svg>
          </div>
        )}

        <div
          style={{
            display: 'grid',
            gridTemplateColumns: `repeat(${cols}, 1fr)`,
            position: 'relative',
          }}
        >
          {stages.map((stage, i) => {
            const status = statuses[i];
            const colors = puckColors(theme, status, isDark);
            const isActive = status === 'active';
            const isNext = status === 'next';
            const isQueued = status === 'queued';
            const versionLabel = stage.iteration > 0 ? `v${stage.iteration}` : '—';

            return (
              <div
                key={stage.id}
                style={{
                  position: 'relative',
                  zIndex: 1,
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'center',
                }}
              >
                <button
                  type="button"
                  disabled={!interactive}
                  onClick={() => handlePick(stage)}
                  aria-label={`${stage.name} — ${stage.headline}`}
                  style={{
                    all: 'unset',
                    cursor: interactive ? 'pointer' : 'default',
                    display: 'flex',
                    flexDirection: 'column',
                    alignItems: 'center',
                    gap: 14,
                    padding: '6px 8px',
                    borderRadius: 12,
                    transition: 'background .15s cubic-bezier(.2,.7,.2,1)',
                  }}
                  onMouseEnter={(e) => {
                    if (interactive) (e.currentTarget as HTMLElement).style.background = sunk;
                  }}
                  onMouseLeave={(e) => {
                    (e.currentTarget as HTMLElement).style.background = 'transparent';
                  }}
                >
                  <div
                    style={{
                      width: NODE_SIZE,
                      height: NODE_SIZE,
                      borderRadius: 999,
                      display: 'flex',
                      flexDirection: 'column',
                      alignItems: 'center',
                      justifyContent: 'center',
                      gap: 3,
                      background: colors.bg,
                      boxShadow: isActive
                        ? `inset 0 0 0 1.5px ${colors.border}, 0 0 0 4px ${surface}, 0 4px 14px -6px ${colors.glow ?? colors.border}`
                        : `inset 0 0 0 1.5px ${colors.border}`,
                      color: colors.fg,
                      position: 'relative',
                      transform: isActive ? 'scale(1.06)' : 'scale(1)',
                      transition: 'all .3s cubic-bezier(.2,.7,.2,1)',
                    }}
                  >
                    {isNext && (
                      <span
                        aria-hidden
                        style={{
                          position: 'absolute',
                          inset: -5,
                          borderRadius: 999,
                          border: `1.5px dashed ${accent}`,
                          opacity: 0.45,
                        }}
                      />
                    )}
                    {isActive && (
                      <span
                        aria-hidden
                        style={{
                          position: 'absolute',
                          inset: -6,
                          borderRadius: 999,
                          border: `1.5px solid ${accent}`,
                          opacity: 0,
                          animation:
                            'asdlc-ps-ring 1.8s cubic-bezier(.2,.7,.2,1) infinite',
                        }}
                      />
                    )}
                    <span
                      style={{
                        font: `500 15px/1 ${monoFamily}`,
                        letterSpacing: '-0.02em',
                      }}
                    >
                      {versionLabel}
                    </span>
                    <span
                      style={{
                        font: `500 9px/1 ${monoFamily}`,
                        letterSpacing: '0.08em',
                        textTransform: 'uppercase',
                        opacity: 0.7,
                      }}
                    >
                      {subLabel(status)}
                    </span>
                  </div>

                  <div
                    style={{
                      textAlign: 'center',
                      display: 'flex',
                      flexDirection: 'column',
                      gap: 4,
                      maxWidth: 180,
                    }}
                  >
                    <div
                      style={{
                        font: `500 14px/1.15 ${fontFamily}`,
                        letterSpacing: '-0.015em',
                        color: isQueued ? muted : text,
                      }}
                    >
                      {stage.name}
                    </div>
                    {stage.headline && (
                      <div
                        style={{
                          font: `400 12px/1.3 ${fontFamily}`,
                          color: isQueued ? subtle : muted,
                        }}
                      >
                        {stage.headline}
                      </div>
                    )}
                    {stage.timestamp && (
                      <span
                        style={{
                          display: 'inline-flex',
                          alignItems: 'center',
                          justifyContent: 'center',
                          gap: 5,
                          marginTop: 2,
                          font: `500 10.5px/1 ${monoFamily}`,
                          letterSpacing: '0.04em',
                          color: subtle,
                        }}
                      >
                        <svg
                          width="9"
                          height="9"
                          viewBox="0 0 9 9"
                          fill="none"
                          stroke="currentColor"
                          strokeWidth="1.3"
                          strokeLinecap="round"
                          strokeLinejoin="round"
                          style={{ display: 'block', opacity: 0.8 }}
                        >
                          <circle cx="4.5" cy="4.5" r="3.2" />
                          <path d="M4.5 2.8v1.7l1.1.9" />
                        </svg>
                        {stage.timestamp}
                      </span>
                    )}
                  </div>
                </button>
              </div>
            );
          })}
        </div>
      </div>

      {interactive && !onStageClick && (
        <StageDrawer
          stage={picked}
          open={picked !== null}
          onClose={() => setPicked(null)}
          mode={mode}
        />
      )}
    </div>
  );
}
