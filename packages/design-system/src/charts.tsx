import { useEffect, useId, useRef } from 'react';
import { init, use } from 'echarts/core';
import { LineChart, BarChart } from 'echarts/charts';
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
  GridComponent,
  TooltipComponent,
  AriaComponent,
  LegendComponent,
  SVGRenderer,
]);

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
    chart.setOption({
      animation: !motion.matches,
      animationDuration: 200,
      aria: { enabled: true, decal: { show: true }, label: { description } },
      color: [
        '#315bd6',
        '#21857b',
        '#9962c2',
        '#bf762c',
        '#c45279',
        '#59798d',
        '#7a883c',
        '#7067b2',
        '#a46550',
        '#4388aa',
      ],
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
        left: kind === 'funnel' ? 12 : 48,
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
      xAxis:
        kind === 'funnel'
          ? axes
          : timeAxis
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
                  hideOverlap: true,
                  width: 100,
                  overflow: 'truncate',
                  color: '#6b7280',
                  fontSize: 11,
                },
              },
      yAxis:
        kind === 'funnel'
          ? {
              type: 'category',
              inverse: true,
              data: entries.map(({ category }) => category.label),
              axisLabel: { width: 130, overflow: 'truncate', color: '#59606d', fontSize: 12 },
            }
          : axes,
      series: plotSeries,
    });
    // The surrounding image has the chart's complete accessible name; avoid a
    // second anonymous image announcement from the SVG renderer.
    target.current.querySelector('svg')?.setAttribute('aria-hidden', 'true');
    const observer = new ResizeObserver(() => chart.resize());
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
        style={{ height: Math.max(320, kind === 'funnel' ? categories.length * 52 : 320) }}
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
    chart.setOption({
      animation: !motion.matches,
      animationDuration: 200,
      aria: {
        enabled: true,
        decal: { show: true },
        label: { description: `${label}。完整数值见下方结果表。` },
      },
      color: ['#2563eb'],
      grid: {
        top: 24,
        left: kind === 'funnel' ? 16 : 56,
        right: 28,
        bottom: 38,
        containLabel: kind === 'funnel',
      },
      tooltip: {
        trigger: 'axis',
        renderMode: 'richText',
        confine: true,
        axisPointer:
          kind === 'trend'
            ? {
                label: {
                  formatter: (params: { value: number | string }) =>
                    timeLabel(Number(params.value)),
                },
              }
            : undefined,
      },
      xAxis:
        kind === 'trend'
          ? {
              type: 'time',
              name: timezone,
              nameLocation: 'middle',
              nameGap: 28,
              axisLabel: {
                hideOverlap: true,
                formatter: (value: number) => timeLabel(new Date(value).toISOString()),
              },
            }
          : { type: 'value', min: 0, minInterval: 1 },
      yAxis:
        kind === 'trend'
          ? { type: 'value', min: 0, minInterval: 1, name: label }
          : {
              type: 'category',
              inverse: true,
              data: categories,
              axisLabel: { width: 150, overflow: 'truncate' },
            },
      series: [
        {
          name: label,
          type: kind === 'trend' ? 'line' : 'bar',
          data: rows.map((row) =>
            kind === 'trend'
              ? [Date.parse(String(row.bucket)), numeric(row.metric_0)]
              : numeric(row.subjects),
          ),
          encode: kind === 'trend' ? { x: 0, y: 1, tooltip: 1 } : undefined,
          connectNulls: false,
          showSymbol: rows.length < 80,
          symbolSize: 6,
          barMaxWidth: 36,
          label: { show: kind === 'funnel', position: 'right' },
          emphasis: { disabled: true },
        },
      ],
    });
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
        height: Math.max(300, kind === 'funnel' ? rows.length * 52 : 300),
        width: '100%',
        minWidth: 0,
      }}
      role="img"
      aria-label={`${label}，完整数值见下方结果表`}
    />
  );
}
