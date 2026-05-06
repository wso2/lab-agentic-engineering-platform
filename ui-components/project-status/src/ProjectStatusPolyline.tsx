import { useMemo, useState, useId } from 'react';
import { useTheme, alpha } from '@mui/material/styles';
import { StageDrawer } from './StageDrawer.js';
import { resolveStateMeta } from './stateMeta.js';
import type { ProjectStatusPolylineProps, Stage } from './types.js';

const W = 1400;
const H = 420;
const PAD_X = 160;
const BASELINE_Y = 320;
const PEAK_Y = 110;

export function ProjectStatusPolyline({
  stages,
  onStageClick,
  interactive = true,
  mode,
  maxIteration,
}: ProjectStatusPolylineProps) {
  const theme = useTheme();
  const gradientId = useId();
  const effectiveMode = mode ?? theme.palette.mode;
  const isDark = effectiveMode === 'dark';

  const [picked, setPicked] = useState<Stage | null>(null);

  const surface = theme.palette.background.paper;
  const text = theme.palette.text.primary;
  const muted = theme.palette.text.secondary;
  const subtle = theme.palette.text.disabled;
  const border = theme.palette.divider;
  const lineColor = alpha(theme.palette.text.primary, isDark ? 0.1 : 0.09);
  const accent = theme.palette.primary.main;
  const fontFamily = theme.typography.fontFamily;
  const radius = typeof theme.shape.borderRadius === 'number' ? theme.shape.borderRadius * 2 : 16;

  // Auto-scale the y-axis to the actual data range so the polyline fills the
  // available height. The widget's outer dimensions are still fixed (1400×420);
  // only the iteration→y mapping rescales. An explicit `maxIteration` prop
  // overrides auto-scale (acts as a ceiling). We never go below 1 to avoid
  // division by zero and to give a single-iteration data set a visible peak.
  const effectiveMax = useMemo(() => {
    if (typeof maxIteration === 'number' && maxIteration > 0) return maxIteration;
    const dataMax = stages.reduce(
      (acc, s) => (s.state === 'pending' ? acc : Math.max(acc, s.iteration)),
      0,
    );
    return Math.max(dataMax, 1);
  }, [stages, maxIteration]);

  const innerW = W - PAD_X * 2;
  const denom = Math.max(stages.length - 1, 1);
  const positions = stages.map((s, i) => {
    const x = stages.length === 1 ? W / 2 : PAD_X + (innerW * i) / denom;
    const norm = s.state === 'pending' ? 0 : Math.min(s.iteration / effectiveMax, 1);
    const y = BASELINE_Y - norm * (BASELINE_Y - PEAK_Y);
    return { x, y, stage: s };
  });

  const linePath = useMemo(() => {
    if (positions.length < 2) return '';
    return positions.map((p, i) => (i === 0 ? `M ${p.x} ${p.y}` : `L ${p.x} ${p.y}`)).join(' ');
  }, [positions]);

  const areaPath = useMemo(() => {
    if (positions.length < 2) return '';
    const last = positions[positions.length - 1];
    const first = positions[0];
    return `${linePath} L ${last.x} ${BASELINE_Y} L ${first.x} ${BASELINE_Y} Z`;
  }, [linePath, positions]);

  const handlePick = (stage: Stage) => {
    if (!interactive) return;
    if (onStageClick) {
      onStageClick(stage);
      return;
    }
    setPicked(stage);
  };

  return (
    <div
      style={{
        background: surface,
        color: text,
        padding: '40px 56px 44px',
        fontFamily,
        borderRadius: radius,
        border: `1px solid ${border}`,
        boxShadow: isDark
          ? '0 1px 0 rgba(255,255,255,0.03) inset'
          : '0 1px 2px rgba(0,0,0,0.02)',
        position: 'relative',
        boxSizing: 'border-box',
      }}
    >
      <div style={{ position: 'relative' }}>
        <svg
          viewBox={`0 0 ${W} ${H}`}
          width="100%"
          style={{ display: 'block', overflow: 'visible' }}
          role="img"
          aria-label="Project status overview"
        >
          <defs>
            <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={accent} stopOpacity={isDark ? 0.22 : 0.15} />
              <stop offset="100%" stopColor={accent} stopOpacity="0" />
            </linearGradient>
          </defs>

          <line
            x1={PAD_X - 40}
            y1={BASELINE_Y}
            x2={W - PAD_X + 40}
            y2={BASELINE_Y}
            stroke={lineColor}
            strokeWidth={1}
          />

          {[1 / 3, 2 / 3].map((frac) => {
            const y = BASELINE_Y - frac * (BASELINE_Y - PEAK_Y);
            return (
              <line
                key={frac}
                x1={PAD_X - 40}
                y1={y}
                x2={W - PAD_X + 40}
                y2={y}
                stroke={lineColor}
                strokeWidth={1}
                strokeDasharray="1 6"
                opacity={0.7}
              />
            );
          })}

          {areaPath && <path d={areaPath} fill={`url(#${gradientId})`} />}
          {linePath && (
            <path
              d={linePath}
              fill="none"
              stroke={accent}
              strokeWidth={2.25}
              strokeLinejoin="round"
              strokeLinecap="round"
            />
          )}

          {positions.map((p, i) => {
            const meta = resolveStateMeta(theme, p.stage.state);
            const isActive = p.stage.state === 'active';
            const isPending = p.stage.state === 'pending';
            const stepLabel = `STAGE ${String(i + 1).padStart(2, '0')}`;

            return (
              <g
                key={p.stage.id}
                style={interactive ? { cursor: 'pointer' } : undefined}
                onClick={interactive ? () => handlePick(p.stage) : undefined}
                tabIndex={interactive ? 0 : undefined}
                role={interactive ? 'button' : undefined}
                aria-label={
                  interactive
                    ? `${p.stage.name} — ${meta.label}, version ${p.stage.iteration}`
                    : undefined
                }
                onKeyDown={
                  interactive
                    ? (e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault();
                          handlePick(p.stage);
                        }
                      }
                    : undefined
                }
              >
                <line
                  x1={p.x}
                  y1={p.y + 14}
                  x2={p.x}
                  y2={BASELINE_Y - 1}
                  stroke={lineColor}
                  strokeWidth={1}
                  strokeDasharray="1 4"
                />

                <text
                  x={p.x}
                  y={p.y - 56}
                  fontSize={10}
                  fontWeight={600}
                  fill={subtle}
                  textAnchor="middle"
                  letterSpacing={1.4}
                >
                  {stepLabel}
                </text>

                <text
                  x={p.x}
                  y={p.y - 38}
                  fontSize={16}
                  fontWeight={600}
                  fill={text}
                  textAnchor="middle"
                  letterSpacing={-0.3}
                >
                  {p.stage.name}
                </text>

                {isActive && (
                  <>
                    <circle cx={p.x} cy={p.y} r={11} fill={meta.dot} opacity={0.22}>
                      <animate
                        attributeName="r"
                        values="11;26;11"
                        dur="2.2s"
                        repeatCount="indefinite"
                      />
                      <animate
                        attributeName="opacity"
                        values="0.32;0;0.32"
                        dur="2.2s"
                        repeatCount="indefinite"
                      />
                    </circle>
                    <circle cx={p.x} cy={p.y} r={11} fill={meta.dot} opacity={0.18}>
                      <animate
                        attributeName="r"
                        values="11;20;11"
                        dur="2.2s"
                        begin="0.6s"
                        repeatCount="indefinite"
                      />
                      <animate
                        attributeName="opacity"
                        values="0.28;0;0.28"
                        dur="2.2s"
                        begin="0.6s"
                        repeatCount="indefinite"
                      />
                    </circle>
                  </>
                )}

                <circle cx={p.x} cy={p.y} r={11} fill={surface} stroke={meta.dot} strokeWidth={2.5} />
                {isPending ? (
                  <circle cx={p.x} cy={p.y} r={3} fill={meta.dot} opacity={0.5} />
                ) : (
                  <circle cx={p.x} cy={p.y} r={4.5} fill={meta.dot} />
                )}

                <g transform={`translate(${p.x}, ${BASELINE_Y + 36})`}>
                  <text
                    fontSize={26}
                    fontWeight={600}
                    fill={text}
                    textAnchor="middle"
                    letterSpacing={-0.6}
                  >
                    {isPending ? '—' : `v${p.stage.iteration}`}
                  </text>
                  <g transform="translate(0, 18)">
                    <rect x={-32} y={0} width={64} height={20} rx={10} fill={meta.pillBg} />
                    <text
                      x={0}
                      y={14}
                      fontSize={10}
                      fontWeight={600}
                      fill={meta.pillText}
                      textAnchor="middle"
                      textRendering="geometricPrecision"
                      letterSpacing={1}
                    >
                      {meta.label.toUpperCase()}
                    </text>
                  </g>
                  <foreignObject x={-130} y={50} width={260} height={64}>
                    <div
                      style={{
                        fontSize: 13,
                        color: muted,
                        lineHeight: 1.45,
                        textAlign: 'center',
                        fontFamily,
                      }}
                    >
                      {p.stage.headline}
                    </div>
                  </foreignObject>
                </g>
              </g>
            );
          })}
        </svg>
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
