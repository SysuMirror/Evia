import { useEffect, useRef } from 'react';
import { init, use } from 'echarts/core';
import { LineChart, BarChart } from 'echarts/charts';
import { GridComponent, TooltipComponent, AriaComponent } from 'echarts/components';
import { SVGRenderer } from 'echarts/renderers';

use([LineChart, BarChart, GridComponent, TooltipComponent, AriaComponent, SVGRenderer]);
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
