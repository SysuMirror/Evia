import { Component, lazy, Suspense, type ReactNode } from 'react';
import type { QueryResult } from '@evia/api-client';
import { LoadingPanel } from '@evia/design-system';
import type { SavedAnalysisChartProps } from '@evia/design-system/charts';
import { formatTime } from './analysis-model';

const SavedChart = lazy(() =>
  import('@evia/design-system/charts').then((module) => ({ default: module.SavedAnalysisChart })),
);

class VisualChartBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    return this.state.failed ? (
      <p role="status">图表暂时无法加载，请查看下方结果表。</p>
    ) : (
      this.props.children
    );
  }
}

function finiteNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function formatPct(value: number, digits = 1): string {
  return `${(value * 100).toFixed(digits)}%`;
}

function formatCompactDate(raw: unknown, timezone: string): string {
  if (typeof raw !== 'string' || !raw) return String(raw ?? '—');
  const ts = Date.parse(raw);
  if (!Number.isFinite(ts)) return raw;
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      timeZone: timezone,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
    }).format(new Date(ts));
  } catch {
    return raw.slice(0, 10);
  }
}

/** Color-coded cohort retention heatmap matrix. */
export function RetentionCohortMatrix({
  rows,
  granularity = 'day',
  timezone,
}: {
  rows: readonly Record<string, unknown>[];
  granularity?: string;
  timezone: string;
}) {
  const maxPeriods = rows.reduce((best, row) => {
    const rates = Array.isArray(row.retention_rate) ? row.retention_rate.length : 0;
    return Math.max(best, rates);
  }, 0);
  if (rows.length === 0 || maxPeriods === 0) return null;

  const prefix = granularity === 'week' ? 'W' : granularity === 'month' ? 'M' : 'D';
  const unitLabel = granularity === 'week' ? '周' : granularity === 'month' ? '月' : '天';

  return (
    <div
      style={{
        border: '1px solid var(--evia-border, #e2e8f0)',
        borderRadius: 12,
        background: 'var(--evia-surface, #ffffff)',
        padding: 16,
        overflowX: 'auto',
      }}
    >
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'baseline',
          marginBottom: 12,
          gap: 12,
          flexWrap: 'wrap',
        }}
      >
        <strong style={{ fontSize: 14, color: 'var(--evia-ink, #0f172a)' }}>
          留存队列热力矩阵（按{unitLabel}）
        </strong>
        <span style={{ fontSize: 12, color: 'var(--evia-muted, #64748b)' }}>
          颜色深浅表示对应期留存率，单元格下方数字为留存主体数
        </span>
      </div>
      <table
        style={{
          width: '100%',
          borderCollapse: 'separate',
          borderSpacing: 4,
          fontSize: 12,
          minWidth: Math.max(480, 200 + maxPeriods * 72),
        }}
      >
        <thead>
          <tr>
            <th
              style={{
                textAlign: 'left',
                padding: '6px 8px',
                color: 'var(--evia-muted, #64748b)',
                fontWeight: 600,
              }}
            >
              队列起始
            </th>
            <th
              style={{
                textAlign: 'right',
                padding: '6px 8px',
                color: 'var(--evia-muted, #64748b)',
                fontWeight: 600,
              }}
            >
              队列规模
            </th>
            {Array.from({ length: maxPeriods }, (_, p) => (
              <th
                key={p}
                style={{
                  textAlign: 'center',
                  padding: '6px 8px',
                  color: 'var(--evia-muted, #64748b)',
                  fontWeight: 600,
                }}
              >
                {prefix}
                {p}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, rowIdx) => {
            const rates = Array.isArray(row.retention_rate) ? row.retention_rate : [];
            const retained = Array.isArray(row.retained) ? row.retained : [];
            const cohortSize = finiteNumber(row.cohort_size) ?? 0;
            return (
              <tr key={rowIdx}>
                <td
                  style={{
                    padding: '6px 8px',
                    whiteSpace: 'nowrap',
                    fontWeight: 500,
                    color: 'var(--evia-ink, #0f172a)',
                  }}
                >
                  {formatCompactDate(row.cohort_start, timezone)}
                </td>
                <td
                  style={{
                    padding: '6px 8px',
                    textAlign: 'right',
                    fontVariantNumeric: 'tabular-nums',
                    color: 'var(--evia-ink, #0f172a)',
                  }}
                >
                  {cohortSize.toLocaleString('zh-CN')}
                </td>
                {Array.from({ length: maxPeriods }, (_, p) => {
                  const rate = finiteNumber(rates[p]);
                  const count = finiteNumber(retained[p]);
                  if (rate === null) {
                    return (
                      <td
                        key={p}
                        style={{
                          textAlign: 'center',
                          padding: '6px 4px',
                          borderRadius: 6,
                          background: '#f8fafc',
                          color: '#94a3b8',
                        }}
                      >
                        —
                      </td>
                    );
                  }
                  const clamped = Math.max(0, Math.min(1, rate));
                  const isZero = clamped === 0 && (count === null || count === 0);
                  const alpha = isZero ? 0 : 0.12 + clamped * 0.76;
                  const darkText = clamped < 0.55;
                  return (
                    <td
                      key={p}
                      title={`${formatCompactDate(row.cohort_start, timezone)} ${prefix}${p}: ${formatPct(clamped, 2)}${count !== null ? ` (${count})` : ''}`}
                      style={{
                        textAlign: 'center',
                        padding: '6px 6px',
                        borderRadius: 6,
                        background: isZero ? '#f8fafc' : `rgba(37, 99, 235, ${alpha.toFixed(3)})`,
                        color: isZero ? '#94a3b8' : darkText ? '#0f172a' : '#ffffff',
                        fontVariantNumeric: 'tabular-nums',
                      }}
                    >
                      <div style={{ fontWeight: 600, lineHeight: 1.25 }}>
                        {formatPct(clamped, 1)}
                      </div>
                      {count !== null && (
                        <div style={{ fontSize: 11, opacity: darkText ? 0.72 : 0.88 }}>
                          {count.toLocaleString('zh-CN')}
                        </div>
                      )}
                    </td>
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** Ranked horizontal proportion bars for segment and attribution analyses. */
export function ProportionBarsVisual({
  title,
  subtitle,
  items,
  accentColor = '#2563eb',
}: {
  title: string;
  subtitle?: string;
  items: readonly {
    key: string;
    label: string;
    badge?: string;
    value: number;
    share: number;
    extra?: string;
  }[];
  accentColor?: string;
}) {
  if (items.length === 0) return null;
  const maxShare = Math.max(0.01, ...items.map((item) => Math.max(0, item.share)));
  const visible = items.slice(0, 15);
  return (
    <div
      style={{
        border: '1px solid var(--evia-border, #e2e8f0)',
        borderRadius: 12,
        background: 'var(--evia-surface, #ffffff)',
        padding: 16,
      }}
    >
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'baseline',
          marginBottom: 14,
          gap: 12,
          flexWrap: 'wrap',
        }}
      >
        <strong style={{ fontSize: 14, color: 'var(--evia-ink, #0f172a)' }}>{title}</strong>
        {subtitle && (
          <span style={{ fontSize: 12, color: 'var(--evia-muted, #64748b)' }}>{subtitle}</span>
        )}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {visible.map((item) => {
          const widthPct = Math.max(2, Math.min(100, (item.share / maxShare) * 100));
          return (
            <div
              key={item.key}
              style={{
                display: 'grid',
                gridTemplateColumns: 'minmax(140px, 220px) 1fr auto',
                alignItems: 'center',
                gap: 12,
              }}
            >
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 6,
                  minWidth: 0,
                  fontSize: 13,
                  color: 'var(--evia-ink, #0f172a)',
                }}
              >
                <span
                  style={{
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    fontWeight: 500,
                  }}
                  title={item.label}
                >
                  {item.label}
                </span>
                {item.badge && (
                  <span
                    style={{
                      fontSize: 11,
                      padding: '1px 6px',
                      borderRadius: 999,
                      background: '#f1f5f9',
                      color: '#475569',
                      flexShrink: 0,
                    }}
                  >
                    {item.badge}
                  </span>
                )}
              </div>
              <div
                style={{
                  height: 18,
                  borderRadius: 6,
                  background: '#f1f5f9',
                  overflow: 'hidden',
                }}
              >
                <div
                  style={{
                    width: `${widthPct.toFixed(1)}%`,
                    height: '100%',
                    borderRadius: 6,
                    background: accentColor,
                  }}
                />
              </div>
              <div
                style={{
                  textAlign: 'right',
                  fontSize: 12,
                  whiteSpace: 'nowrap',
                  fontVariantNumeric: 'tabular-nums',
                  color: 'var(--evia-ink, #0f172a)',
                }}
              >
                <strong>{formatPct(item.share, 1)}</strong>
                <span style={{ marginLeft: 8, color: 'var(--evia-muted, #64748b)' }}>
                  {item.value.toLocaleString('zh-CN')}
                  {item.extra ? ` | ${item.extra}` : ''}
                </span>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** Step-pill flow chart for top behavioral paths. */
export function PathFlowVisual({ rows }: { rows: readonly Record<string, unknown>[] }) {
  const parsed = rows
    .map((row, idx) => {
      const steps = Array.isArray(row.path)
        ? row.path.map((step) => String(step))
        : typeof row.path === 'string'
          ? row.path.split(' → ')
          : [];
      const subjects = finiteNumber(row.subjects) ?? 0;
      const share = finiteNumber(row.share);
      return { key: `${idx}-${steps.join('>')}`, steps, subjects, share };
    })
    .filter((item) => item.steps.length > 0);
  if (parsed.length === 0) return null;

  const maxSubjects = Math.max(1, ...parsed.map((item) => item.subjects));
  const totalSubjects = parsed.reduce((sum, item) => sum + item.subjects, 0);
  const visible = parsed.slice(0, 12);

  return (
    <div
      style={{
        border: '1px solid var(--evia-border, #e2e8f0)',
        borderRadius: 12,
        background: 'var(--evia-surface, #ffffff)',
        padding: 16,
      }}
    >
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'baseline',
          marginBottom: 14,
          gap: 12,
          flexWrap: 'wrap',
        }}
      >
        <strong style={{ fontSize: 14, color: 'var(--evia-ink, #0f172a)' }}>
          高频行为路径流向（Top {visible.length}）
        </strong>
        <span style={{ fontSize: 12, color: 'var(--evia-muted, #64748b)' }}>
          按到达主体数降序展示事件因果链
        </span>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {visible.map((item, idx) => {
          const widthPct = Math.max(4, (item.subjects / maxSubjects) * 100);
          const pct = item.share ?? (totalSubjects > 0 ? item.subjects / totalSubjects : 0);
          return (
            <div key={item.key} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <div
                style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  gap: 12,
                  flexWrap: 'wrap',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                  <span
                    style={{
                      fontSize: 11,
                      fontWeight: 600,
                      color: '#64748b',
                      minWidth: 20,
                    }}
                  >
                    #{idx + 1}
                  </span>
                  {item.steps.map((step, sIdx) => (
                    <span
                      key={`${sIdx}-${step}`}
                      style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}
                    >
                      <span
                        style={{
                          fontSize: 12,
                          padding: '2px 8px',
                          borderRadius: 6,
                          background: sIdx === 0 ? '#eff6ff' : '#f8fafc',
                          border: `1px solid ${sIdx === 0 ? '#bfdbfe' : '#e2e8f0'}`,
                          color: sIdx === 0 ? '#1d4ed8' : '#1e293b',
                          fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                        }}
                      >
                        {step}
                      </span>
                      {sIdx < item.steps.length - 1 && (
                        <span style={{ color: '#94a3b8', fontSize: 12 }}>→</span>
                      )}
                    </span>
                  ))}
                </div>
                <div
                  style={{
                    fontSize: 12,
                    fontVariantNumeric: 'tabular-nums',
                    whiteSpace: 'nowrap',
                    color: 'var(--evia-ink, #0f172a)',
                  }}
                >
                  <strong>{item.subjects.toLocaleString('zh-CN')}</strong>
                  <span style={{ marginLeft: 6, color: 'var(--evia-muted, #64748b)' }}>
                    ({formatPct(pct, 1)})
                  </span>
                </div>
              </div>
              <div
                style={{ height: 8, borderRadius: 4, background: '#f1f5f9', overflow: 'hidden' }}
              >
                <div
                  style={{
                    width: `${widthPct.toFixed(1)}%`,
                    height: '100%',
                    borderRadius: 4,
                    background: '#0d9488',
                  }}
                />
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** Generic multi-series line/bar/KPI renderer for ad-hoc trend and metric/formula runs. */
export function QuerySeriesVisual({
  result,
  label,
  timezone,
  interval,
  timeRange,
  metricLabels,
}: {
  result: QueryResult;
  label: string;
  timezone: string;
  interval?: string;
  timeRange?: { from: number; to: number };
  metricLabels?: readonly string[];
}) {
  const rows = result.rows as Record<string, unknown>[];
  if (rows.length === 0) return null;

  const hasBucket = result.columns.some((col) => col.key === 'bucket');
  const groupCols = result.columns.filter((col) => /^group_\d+$/.test(col.key));
  const metricCols = result.columns.filter(
    (col) => /^metric_\d+$/.test(col.key) || col.key === 'value',
  );
  if (metricCols.length === 0) return null;

  const metricName = (col: { key: string; label?: string }, idx: number) =>
    metricLabels?.[idx] || col.label || (metricCols.length === 1 ? label : col.key);

  const rowGroupLabel = (row: Record<string, unknown>) =>
    groupCols
      .map((col) => {
        const present = row[`${col.key}_present`];
        const value = row[col.key];
        if (present === false || value === undefined) return '缺失';
        if (value === null) return 'null';
        return String(value);
      })
      .join(' / ');

  // Single scalar KPI card(s) when no bucket and no groups
  if (!hasBucket && groupCols.length === 0 && rows.length === 1) {
    return (
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: `repeat(auto-fit, minmax(180px, 1fr))`,
          gap: 12,
        }}
      >
        {metricCols.map((col, idx) => {
          const val = finiteNumber(rows[0]![col.key]);
          return (
            <div key={col.key} className="analysis-stat">
              <div>
                <span>{metricName(col, idx)}</span>
                <div className="metric-value">
                  {val !== null ? val.toLocaleString('zh-CN', { maximumFractionDigits: 4 }) : '—'}
                </div>
              </div>
            </div>
          );
        })}
      </div>
    );
  }

  let chartProps: SavedAnalysisChartProps;
  if (hasBucket) {
    const times = [...new Set(rows.map((row) => String(row.bucket)))].sort(
      (a, b) => Date.parse(a) - Date.parse(b),
    );
    const groups = new Map<string, { label: string; rows: Record<string, unknown>[] }>();
    for (const row of rows) {
      const gLabel = groupCols.length > 0 ? rowGroupLabel(row) : '';
      const entry = groups.get(gLabel) ?? { label: gLabel, rows: [] };
      entry.rows.push(row);
      groups.set(gLabel, entry);
    }
    const series: SavedAnalysisChartProps['series'][number][] = [];
    for (const [gKey, entry] of groups) {
      for (const [mIdx, col] of metricCols.entries()) {
        const byBucket = new Map(
          entry.rows.map((r) => [String(r.bucket), finiteNumber(r[col.key])]),
        );
        const mLabel = metricName(col, mIdx);
        series.push({
          id: `${gKey}:${col.key}`,
          label: entry.label ? `${entry.label} | ${mLabel}` : mLabel,
          values: times.map((t) => byBucket.get(t) ?? null),
        });
      }
    }
    const intervalMs = interval
      ? ({ minute: 60000, hour: 3600000, day: 86400000, week: 604800000 }[interval] ?? undefined)
      : undefined;
    chartProps = {
      kind: 'line',
      label,
      timezone,
      categories: times.map((t) => ({
        key: t,
        label: formatTime(t, timezone),
        timestamp: Date.parse(t),
      })),
      series,
      timeRange,
      intervalMs,
    };
  } else {
    chartProps = {
      kind: 'bar',
      label,
      timezone,
      categories: rows.map((row, idx) => ({
        key: `${idx}`,
        label: rowGroupLabel(row) || `项 ${idx + 1}`,
      })),
      series: metricCols.map((col, idx) => ({
        id: col.key,
        label: metricName(col, idx),
        values: rows.map((r) => finiteNumber(r[col.key])),
      })),
    };
  }

  return (
    <VisualChartBoundary>
      <Suspense fallback={<LoadingPanel label="正在加载图表" />}>
        <SavedChart {...chartProps} />
      </Suspense>
    </VisualChartBoundary>
  );
}

/** Lift & 95% confidence interval forest plot for A/B experiment variants. */
export function ExperimentForestPlot({
  rows,
  metricMode,
}: {
  rows: readonly {
    variant: string;
    is_control: boolean;
    lift_absolute: number;
    ci_lower: number;
    ci_upper: number;
    significant: boolean;
    p_value: number;
  }[];
  metricMode: 'conversion' | 'numeric_mean';
}) {
  const treatments = rows.filter((r) => !r.is_control);
  if (treatments.length === 0) return null;

  const allPoints = [
    0,
    ...treatments.flatMap((t) => [t.ci_lower, t.ci_upper, t.lift_absolute]),
  ].filter((n) => Number.isFinite(n));
  const minVal = Math.min(...allPoints);
  const maxVal = Math.max(...allPoints);
  const span = Math.max(maxVal - minVal, metricMode === 'conversion' ? 0.05 : 1);
  const pad = span * 0.14;
  const domainMin = minVal - pad;
  const domainMax = maxVal + pad;
  const toX = (val: number) => ((val - domainMin) / (domainMax - domainMin)) * 100;
  const zeroX = toX(0);

  const fmt = (v: number) =>
    metricMode === 'conversion'
      ? `${v > 0 ? '+' : ''}${(v * 100).toFixed(2)}%`
      : `${v > 0 ? '+' : ''}${v.toFixed(2)}`;

  return (
    <div
      style={{
        border: '1px solid var(--evia-border, #e2e8f0)',
        borderRadius: 10,
        background: '#fafbfd',
        padding: '14px 16px',
      }}
    >
      <div
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'baseline',
          marginBottom: 12,
          gap: 12,
          flexWrap: 'wrap',
        }}
      >
        <strong style={{ fontSize: 13, color: 'var(--evia-ink, #0f172a)' }}>
          相对对照提升与 95% 置信区间（Forest Plot）
        </strong>
        <span style={{ fontSize: 12, color: 'var(--evia-muted, #64748b)' }}>
          虚线为 0 基准线；区间完全位于 0 右侧代表统计显著正向提升
        </span>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {treatments.map((t) => {
          const leftPct = Math.max(0, Math.min(100, toX(t.ci_lower)));
          const rightPct = Math.max(0, Math.min(100, toX(t.ci_upper)));
          const midPct = Math.max(0, Math.min(100, toX(t.lift_absolute)));
          const toneColor = t.significant
            ? t.lift_absolute >= 0
              ? '#059669'
              : '#dc2626'
            : '#64748b';
          return (
            <div
              key={t.variant}
              style={{
                display: 'grid',
                gridTemplateColumns: 'minmax(100px, 140px) 1fr minmax(180px, 220px)',
                alignItems: 'center',
                gap: 14,
              }}
            >
              <div style={{ fontSize: 13, fontWeight: 600, color: '#0f172a' }}>{t.variant}</div>
              <div style={{ position: 'relative', height: 28 }}>
                <div
                  style={{
                    position: 'absolute',
                    left: 0,
                    right: 0,
                    top: '50%',
                    height: 1,
                    background: '#e2e8f0',
                  }}
                />
                <div
                  style={{
                    position: 'absolute',
                    left: `${zeroX.toFixed(2)}%`,
                    top: 0,
                    bottom: 0,
                    borderLeft: '1.5px dashed #94a3b8',
                  }}
                />
                <div
                  style={{
                    position: 'absolute',
                    left: `${leftPct.toFixed(2)}%`,
                    width: `${Math.max(0.8, rightPct - leftPct).toFixed(2)}%`,
                    top: '50%',
                    height: 4,
                    marginTop: -2,
                    borderRadius: 2,
                    background: toneColor,
                    opacity: 0.78,
                  }}
                />
                <div
                  style={{
                    position: 'absolute',
                    left: `${leftPct.toFixed(2)}%`,
                    top: '50%',
                    height: 12,
                    marginTop: -6,
                    width: 2,
                    background: toneColor,
                  }}
                />
                <div
                  style={{
                    position: 'absolute',
                    left: `${rightPct.toFixed(2)}%`,
                    top: '50%',
                    height: 12,
                    marginTop: -6,
                    width: 2,
                    background: toneColor,
                  }}
                />
                <div
                  style={{
                    position: 'absolute',
                    left: `${midPct.toFixed(2)}%`,
                    top: '50%',
                    width: 10,
                    height: 10,
                    marginLeft: -5,
                    marginTop: -5,
                    borderRadius: '50%',
                    background: toneColor,
                    border: '2px solid #ffffff',
                    boxShadow: '0 0 0 1px rgba(15,23,42,0.12)',
                  }}
                />
              </div>
              <div
                style={{
                  textAlign: 'right',
                  fontSize: 12,
                  fontVariantNumeric: 'tabular-nums',
                  color: '#0f172a',
                  whiteSpace: 'nowrap',
                }}
              >
                <strong style={{ color: toneColor }}>{fmt(t.lift_absolute)}</strong>
                <span style={{ marginLeft: 8, color: '#64748b' }}>
                  [{fmt(t.ci_lower)}, {fmt(t.ci_upper)}]
                </span>
              </div>
            </div>
          );
        })}
      </div>
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'minmax(100px, 140px) 1fr minmax(180px, 220px)',
          gap: 14,
          marginTop: 6,
          fontSize: 11,
          color: '#94a3b8',
        }}
      >
        <span />
        <div style={{ display: 'flex', justifyContent: 'space-between', position: 'relative' }}>
          <span>{fmt(domainMin)}</span>
          <span
            style={{
              position: 'absolute',
              left: `${zeroX.toFixed(2)}%`,
              transform: 'translateX(-50%)',
              color: '#475569',
              fontWeight: 600,
            }}
          >
            0
          </span>
          <span>{fmt(domainMax)}</span>
        </div>
        <span />
      </div>
    </div>
  );
}
