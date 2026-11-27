import { useEffect, useId, useRef } from 'react';
import { init, use } from 'echarts/core';
import { LineChart, BarChart, FunnelChart } from 'echarts/charts';
import {
  GridComponent,
  TooltipComponent,
  AriaComponent,
  LegendComponent,
} from 'echarts/components';
import { SVGRenderer } from 'echarts/renderers';

use([
  LineChart,
  BarChart,
  FunnelChart,
  GridComponent,
  TooltipComponent,
  AriaComponent,
  LegendComponent,
  SVGRenderer,
]);

const FUNNEL_PALETTE = [
  '#1d4ed8',
  '#2563eb',
  '#3b82f6',
  '#0d9488',
  '#14b8a6',
  '#4f46e5',
  '#6366f1',
  '#0284c7',
];

function formatPercent(numerator: number, denominator: number): string {
  if (!Number.isFinite(numerator) || !Number.isFinite(denominator) || denominator <= 0) {
    return '0.0%';
  }
  return `${((numerator / denominator) * 100).toFixed(1)}%`;
}

export interface SavedChartCategory {
  key: string;
  label: string;
  timestamp?: number;
}

export interface SavedChartSeries {
  id: string;
  label: string;
  unit?: string;
  /** Values align with categories. Absence is null, including a missing group in a bucket. */
  values: readonly (number | null)[];
}

export interface SavedAnalysisChartProps {
  kind: 'line' | 'bar' | 'funnel';
  label: string;
  timezone: string;
  categories: readonly SavedChartCategory[];
  series: readonly SavedChartSeries[];
  timeRange?: { from: number; to: number };
  intervalMs?: number;
}

function finite(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

/** The caller owns result-column and dimension semantics; this component only plots normalized data. */
export function SavedAnalysisChart({
  kind,
  label,
  timezone,
  categories,
  series,
  timeRange,
  intervalMs,
}: SavedAnalysisChartProps) {
  const target = useRef<HTMLDivElement>(null);
  const partialID = useId();
  const partialBucket =
    kind !== 'funnel' &&
    categories.length > 0 &&
    categories.every((category) => finite(category.timestamp) !== null) &&
    timeRange !== undefined &&
    finite(timeRange.from) !== null &&
    categories.some((category) => category.timestamp! < timeRange.from);
  const description = `${label}，${kind === 'line' ? '折线图' : kind === 'funnel' ? '漏斗图' : '柱状图'}，${series.map((item) => `${item.label}${item.unit ? `（${item.unit}）` : ''}`).join('，')}，${timezone}，完整数值见下方结果表`;
  useEffect(() => {
    if (!target.current) return;
    const chart = init(target.current, undefined, { renderer: 'svg' });
    const motion = window.matchMedia('(prefers-reduced-motion: reduce)');
    let formatter: Intl.DateTimeFormat;
    let fullFormatter: Intl.DateTimeFormat;
    try {
      formatter = new Intl.DateTimeFormat('zh-CN', {
        timeZone: timezone,
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        hourCycle: 'h23',
      });
      fullFormatter = new Intl.DateTimeFormat('zh-CN', {
        timeZone: timezone,
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        hourCycle: 'h23',
        timeZoneName: 'shortOffset',
      });
    } catch {
      formatter = new Intl.DateTimeFormat('zh-CN', { timeZone: 'UTC' });
      fullFormatter = formatter;
    }
    const timeLabel = (timestamp: number) =>
      Number.isFinite(timestamp) ? formatter.format(timestamp) : '';
    const timeAxis =
      kind !== 'funnel' &&
      categories.length > 0 &&
      categories.every((category) => finite(category.timestamp) !== null);
    const entries = categories.map((category, index) => ({ category, index }));
    if (timeAxis) entries.sort((a, b) => a.category.timestamp! - b.category.timestamp!);
    const units = [...new Set(series.map((item) => item.unit ?? ''))];
    const names = series.map((item, index) => {
      const name = `${item.label}${item.unit ? `（${item.unit}）` : ''}`;
      return series.some(
        (candidate, other) =>
          other !== index && candidate.label === item.label && candidate.unit === item.unit,
      )
        ? `${name}（系列 ${index + 1}）`
        : name;
    });
    const range =
      timeRange &&
      finite(timeRange.from) !== null &&
      finite(timeRange.to) !== null &&
      timeRange.from < timeRange.to
        ? timeRange
        : undefined;
    const axes = units.map((unit, index) => {
      const values = series
        .filter((item) => (item.unit ?? '') === unit)
        .flatMap((item) => item.values.map(finite))
        .filter((value): value is number => value !== null);
      return {
        type: 'value' as const,
        name: unit,
        min: values.some((value) => value < 0) ? undefined : 0,
        position: kind === 'funnel' ? 'bottom' : index === 0 ? 'left' : 'right',
        offset: Math.max(0, index - 1) * 48,
        axisLabel: { color: '#6b7280', fontSize: 11 },
        splitLine: { show: index === 0, lineStyle: { color: '#e7e9ed' } },
      };
    });
    const plotSeries = series.map((item, seriesIndex) => {
      const points: (number | null | [number, number | null])[] = [];
      let previousTimestamp: number | undefined;
      for (const { category, index } of entries) {
        const value = finite(item.values[index]);
        if (timeAxis) {
          const timestamp = category.timestamp!;
          // A single null breaks the line across omitted buckets without filling an
          // arbitrarily long range. Daily buckets tolerate the daylight-saving shift.
          const tolerance = intervalMs && intervalMs >= 20 * 60 * 60 * 1000 ? 1.5 : 1.01;
          if (
            kind === 'line' &&
            intervalMs &&
            Number.isFinite(intervalMs) &&
            intervalMs > 0 &&
            previousTimestamp !== undefined &&
            timestamp - previousTimestamp > intervalMs * tolerance
          )
            points.push([previousTimestamp + intervalMs, null]);
          points.push([range ? Math.max(timestamp, range.from) : timestamp, value]);
          previousTimestamp = timestamp;
        } else points.push(value);
      }
      return {
        id: `${seriesIndex}-${item.id}`,
        name: names[seriesIndex],
        type: kind === 'line' ? ('line' as const) : ('bar' as const),
        ...(kind === 'funnel'
          ? { xAxisIndex: units.indexOf(item.unit ?? '') }
          : { yAxisIndex: units.indexOf(item.unit ?? '') }),
        data: points,
        encode: timeAxis ? { x: 0, y: 1, tooltip: 1 } : undefined,
        connectNulls: false,
        showSymbol: entries.length < 80,
        symbolSize: 5,
        barMaxWidth: 36,
        barGap: '12%',
        clip: true,
        label: { show: kind === 'funnel', position: 'right' as const },
        emphasis: { focus: 'series' as const },
      };
    });
    const chartColors = [
      '#2563eb',
      '#0d9488',
      '#6366f1',
      '#d97706',
      '#e11d48',
      '#475569',
      '#65a30d',
      '#7c3aed',
      '#b45309',
      '#0284c7',
    ];
    if (kind === 'funnel') {
      const multi = series.length > 1;
      const funnelSeries = series.map((item, seriesIndex) => {
        const vals = entries.map(({ index }) => finite(item.values[index]) ?? 0);
        const firstVal = vals[0] ?? 0;
        const maxVal = Math.max(1, ...vals);
        const slotWidth = multi ? Math.floor(86 / series.length) : 52;
        const leftPct = multi ? 7 + seriesIndex * Math.floor(88 / series.length) : 10;
        return {
          id: `${seriesIndex}-${item.id}`,
          name: names[seriesIndex],
          type: 'funnel' as const,
          sort: 'none' as const,
          funnelAlign: 'center' as const,
          left: `${leftPct}%`,
          width: `${Math.max(24, slotWidth - (multi ? 6 : 0))}%`,
          top: multi ? 44 : 20,
          bottom: 20,
          min: 0,
          max: maxVal,
          minSize: '20%',
          maxSize: '100%',
          gap: 4,
          label: {
            show: true,
            position: multi ? ('inside' as const) : ('right' as const),
            color: multi ? '#ffffff' : '#1e293b',
            fontSize: 12,
            lineHeight: 18,
            formatter: (params: { name?: string; value?: unknown; dataIndex?: number }) => {
              const idx = typeof params.dataIndex === 'number' ? params.dataIndex : 0;
              const val = finite(params.value) ?? 0;
              const prev = idx > 0 ? (vals[idx - 1] ?? 0) : val;
              const overall = formatPercent(val, firstVal);
              if (multi) {
                return `${params.name ?? ''}\n${val} (${overall})`;
              }
              if (idx === 0) {
                return `${params.name ?? ''}   ${val} (${overall})`;
              }
              const stepConv = formatPercent(val, prev);
              return `${params.name ?? ''}   ${val} (总 ${overall} | 较上步 ${stepConv})`;
            },
          },
          labelLine: {
            show: !multi,
            length: 18,
            lineStyle: { color: '#cbd5e1', width: 1 },
          },
          itemStyle: {
            borderColor: '#ffffff',
            borderWidth: 2,
          },
          emphasis: {
            itemStyle: { opacity: 0.92 },
          },
          data: entries.map(({ category }, idx) => ({
            name: category.label,
            value: vals[idx] ?? 0,
            itemStyle: {
              color: multi
                ? chartColors[seriesIndex % chartColors.length]
                : FUNNEL_PALETTE[idx % FUNNEL_PALETTE.length],
            },
          })),
        };
      });
      chart.setOption({
        animation: !motion.matches,
        animationDuration: 200,
        aria: { enabled: true, decal: { show: false }, label: { description } },
        color: FUNNEL_PALETTE,
        legend: {
          show: multi,
          type: 'scroll',
          top: 0,
          left: 0,
          right: 0,
          padding: 0,
          itemWidth: 12,
          itemHeight: 8,
          textStyle: { color: '#414752', fontSize: 12 },
          data: names,
        },
        tooltip: {
          trigger: 'item',
          renderMode: 'richText',
          confine: true,
          formatter: (params: {
            seriesIndex?: number;
            dataIndex?: number;
            name?: string;
            value?: unknown;
          }) => {
            const sIdx = typeof params.seriesIndex === 'number' ? params.seriesIndex : 0;
            const dIdx = typeof params.dataIndex === 'number' ? params.dataIndex : 0;
            const src = series[sIdx];
            const vals = entries.map(({ index }) => finite(src?.values[index]) ?? 0);
            const val = finite(params.value) ?? 0;
            const firstVal = vals[0] ?? 0;
            const prevVal = dIdx > 0 ? (vals[dIdx - 1] ?? 0) : val;
            const unit = src?.unit ? ` ${src.unit}` : '';
            const lines = [
              `${params.name ?? ''}`,
              `${names[sIdx] ?? ''}: ${val}${unit}`,
              `相对首步转化: ${formatPercent(val, firstVal)}`,
            ];
            if (dIdx > 0) {
              lines.push(`较上一步转化: ${formatPercent(val, prevVal)}`);
              lines.push(`本步流失: ${Math.max(0, prevVal - val)}${unit}`);
            }
            return lines.join('\n');
          },
        },
        series: funnelSeries,
      });
    } else {
      chart.setOption({
        animation: !motion.matches,
        animationDuration: 200,
        aria: { enabled: true, decal: { show: false }, label: { description } },
        color: chartColors,
        legend: {
          show: series.length > 1,
          type: 'scroll',
          top: 0,
          left: 0,
          right: 0,
          padding: 0,
          itemWidth: 12,
          itemHeight: 8,
          textStyle: { color: '#414752', fontSize: 12 },
          data: names,
        },
        grid: {
          top: series.length > 1 ? 52 : 28,
          left: 48,
          right: 24 + Math.max(0, units.length - 1) * 48,
          bottom: timeAxis ? 56 : 32,
          containLabel: true,
        },
        tooltip: {
          trigger: 'axis',
          renderMode: 'richText',
          confine: true,
          axisPointer: timeAxis
            ? {
                label: {
                  formatter: (params: { value: number | string }) =>
                    fullFormatter.format(Number(params.value)),
                },
              }
            : undefined,
        },
        xAxis: timeAxis
          ? {
              type: 'time',
              min: range?.from,
              max: range?.to,
              name: timezone,
              nameLocation: 'middle',
              nameGap: 34,
              axisLabel: {
                hideOverlap: true,
                color: '#6b7280',
                fontSize: 11,
                formatter: timeLabel,
              },
            }
          : {
              type: 'category',
              data: entries.map(({ category }) => category.label),
              axisLabel: {
                interval: 0,
                hideOverlap: false,
                width: 100,
                overflow: 'truncate',
                color: '#6b7280',
                fontSize: 11,
              },
            },
        yAxis: axes,
        series: plotSeries,
      });
    }
    const fitCategoryLabels = () => {
      if (kind === 'funnel' || timeAxis || entries.length === 0) return;
      const first = chart.convertToPixel({ xAxisIndex: 0 }, 0);
      const second = entries.length > 1 ? chart.convertToPixel({ xAxisIndex: 0 }, 1) : undefined;
      const slot =
        typeof first === 'number' && typeof second === 'number'
          ? Math.abs(second - first)
          : Math.max(32, chart.getWidth() - 144);
      if (!Number.isFinite(slot) || slot <= 0) return;
      const stride = Math.max(1, Math.ceil(48 / slot));
      chart.setOption({
        xAxis: {
          axisLabel: {
            interval: stride - 1,
            width: Math.max(28, Math.min(110, slot * stride - 8)),
            hideOverlap: false,
            showMinLabel: true,
            showMaxLabel: stride === 1,
          },
        },
      });
    };
    fitCategoryLabels();
    // The surrounding image has the chart's complete accessible name; avoid a
    // second anonymous image announcement from the SVG renderer.
    target.current.querySelector('svg')?.setAttribute('aria-hidden', 'true');
    const observer = new ResizeObserver(() => {
      chart.resize();
      fitCategoryLabels();
    });
    observer.observe(target.current);
    return () => {
      observer.disconnect();
      chart.dispose();
    };
  }, [kind, label, timezone, categories, series, timeRange, intervalMs, description]);
  return (
    <div className="saved-analysis-chart-wrap">
      <div
        ref={target}
        className="chart-surface saved-analysis-chart"
        role="img"
        aria-label={description}
        aria-describedby={partialBucket ? partialID : undefined}
        style={{ height: Math.max(320, kind === 'funnel' ? categories.length * 80 : 320) }}
      />
      {partialBucket && (
        <p className="field-help saved-analysis-chart-note" id={partialID}>
          首个时间桶按本次范围起点绘制，仅统计范围内的数据，原始桶起点见下方结果表。
        </p>
      )}
    </div>
  );
}
export interface AnalysisChartProps {
  kind: 'trend' | 'funnel';
  rows: readonly Record<string, unknown>[];
  label: string;
  timezone: string;
  steps?: readonly string[];
}
export function AnalysisChart({ kind, rows, label, timezone, steps }: AnalysisChartProps) {
  const target = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!target.current) return;
    const chart = init(target.current, undefined, { renderer: 'svg' });
    const motion = window.matchMedia('(prefers-reduced-motion: reduce)');
    const timeLabel = (value: unknown) => {
      const date = new Date(typeof value === 'number' ? value : String(value));
      return Number.isFinite(date.getTime())
        ? new Intl.DateTimeFormat('zh-CN', {
            timeZone: timezone,
            year: 'numeric',
            timeZoneName: 'shortOffset',
            month: '2-digit',
            day: '2-digit',
            hour: '2-digit',
            minute: '2-digit',
            hourCycle: 'h23',
          }).format(date)
        : String(value);
    };
    const numeric = (value: unknown): number | null =>
      typeof value === 'number' && Number.isFinite(value) ? value : null;
    const categories = rows.map((row, index) =>
      kind === 'trend'
        ? timeLabel(row.bucket)
        : (steps?.[index] ?? String(row.event_key ?? `步骤 ${index + 1}`)),
    );
    if (kind === 'funnel') {
      const stepValues = rows.map((row) => numeric(row.subjects) ?? 0);
      const firstVal = stepValues[0] ?? 0;
      const maxVal = Math.max(1, ...stepValues);
      chart.setOption({
        animation: !motion.matches,
        animationDuration: 200,
        aria: {
          enabled: true,
          decal: { show: false },
          label: { description: `${label}。完整数值见下方结果表。` },
        },
        color: FUNNEL_PALETTE,
        tooltip: {
          trigger: 'item',
          renderMode: 'richText',
          confine: true,
          formatter: (params: { dataIndex?: number; name?: string; value?: unknown }) => {
            const idx = typeof params.dataIndex === 'number' ? params.dataIndex : 0;
            const val = numeric(params.value) ?? 0;
            const prev = idx > 0 ? (stepValues[idx - 1] ?? 0) : val;
            const lines = [
              `${params.name ?? ''}`,
              `到达主体: ${val}`,
              `相对首步转化: ${formatPercent(val, firstVal)}`,
            ];
            if (idx > 0) {
              lines.push(`较上一步转化: ${formatPercent(val, prev)}`);
              lines.push(`本步流失: ${Math.max(0, prev - val)}`);
            }
            return lines.join('\n');
          },
        },
        series: [
          {
            name: label,
            type: 'funnel',
            sort: 'none',
            funnelAlign: 'center',
            left: '8%',
            width: '50%',
            top: 20,
            bottom: 20,
            min: 0,
            max: maxVal,
            minSize: '22%',
            maxSize: '100%',
            gap: 5,
            label: {
              show: true,
              position: 'right',
              color: '#0f172a',
              fontSize: 13,
              lineHeight: 20,
              formatter: (params: { dataIndex?: number; name?: string; value?: unknown }) => {
                const idx = typeof params.dataIndex === 'number' ? params.dataIndex : 0;
                const val = numeric(params.value) ?? 0;
                const prev = idx > 0 ? (stepValues[idx - 1] ?? 0) : val;
                const overall = formatPercent(val, firstVal);
                if (idx === 0) {
                  return `${params.name ?? ''}   ${val} (${overall})`;
                }
                const stepConv = formatPercent(val, prev);
                return `${params.name ?? ''}   ${val} (总 ${overall} | 较上步 ${stepConv})`;
              },
            },
            labelLine: {
              show: true,
              length: 22,
              lineStyle: { color: '#cbd5e1', width: 1.2 },
            },
            itemStyle: {
              borderColor: '#ffffff',
              borderWidth: 2,
            },
            emphasis: {
              itemStyle: { opacity: 0.92 },
            },
            data: categories.map((stepName, index) => ({
              name: stepName,
              value: stepValues[index] ?? 0,
              itemStyle: { color: FUNNEL_PALETTE[index % FUNNEL_PALETTE.length] },
            })),
          },
        ],
      });
    } else {
      chart.setOption({
        animation: !motion.matches,
        animationDuration: 200,
        aria: {
          enabled: true,
          decal: { show: false },
          label: { description: `${label}。完整数值见下方结果表。` },
        },
        color: ['#2563eb'],
        grid: {
          top: 24,
          left: 56,
          right: 28,
          bottom: 38,
          containLabel: false,
        },
        tooltip: {
          trigger: 'axis',
          renderMode: 'richText',
          confine: true,
          axisPointer: {
            label: {
              formatter: (params: { value: number | string }) => timeLabel(Number(params.value)),
            },
          },
        },
        xAxis: {
          type: 'time',
          name: timezone,
          nameLocation: 'middle',
          nameGap: 28,
          axisLabel: {
            hideOverlap: true,
            formatter: (value: number) => timeLabel(new Date(value).toISOString()),
          },
        },
        yAxis: { type: 'value', min: 0, minInterval: 1, name: label },
        series: [
          {
            name: label,
            type: 'line',
            data: rows.map((row) => [Date.parse(String(row.bucket)), numeric(row.metric_0)]),
            encode: { x: 0, y: 1, tooltip: 1 },
            connectNulls: false,
            showSymbol: rows.length < 80,
            symbolSize: 6,
            emphasis: { disabled: true },
          },
        ],
      });
    }
    target.current.querySelector('svg')?.setAttribute('aria-hidden', 'true');
    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(target.current);
    return () => {
      observer.disconnect();
      chart.dispose();
    };
  }, [kind, rows, label, timezone, steps]);
  return (
    <div
      ref={target}
      className="chart-surface"
      style={{
        height: Math.max(320, kind === 'funnel' ? rows.length * 88 : 300),
        width: '100%',
        minWidth: 0,
      }}
      role="img"
      aria-label={`${label}，完整数值见下方结果表`}
    />
  );
}
