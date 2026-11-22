import { SavedAnalysisChart } from '@evia/design-system/charts';
import type { ProjectOverview } from '@evia/api-client';

export function OverviewTrend({
  overview,
  timezone,
}: {
  overview: ProjectOverview;
  timezone: string;
}) {
  return (
    <SavedAnalysisChart
      kind="line"
      label="项目事件趋势"
      timezone={timezone}
      categories={overview.trend.map((point) => ({
        key: point.bucket,
        label: point.bucket,
        timestamp: Date.parse(point.bucket),
      }))}
      series={[
        {
          id: 'events',
          label: '事件数',
          unit: '次',
          values: overview.trend.map((point) => point.events),
        },
      ]}
      timeRange={{ from: Date.parse(overview.from), to: Date.parse(overview.to) }}
      intervalMs={overview.interval === 'day' ? 24 * 60 * 60 * 1000 : 60 * 60 * 1000}
    />
  );
}
