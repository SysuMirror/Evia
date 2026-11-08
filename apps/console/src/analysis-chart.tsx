import { lazy, Suspense, Component, type ReactNode } from 'react';
import { LoadingPanel } from '@evia/design-system';
import type { AnalysisChartProps } from '@evia/design-system/charts';
const Chart = lazy(() =>
  import('@evia/design-system/charts').then((module) => ({ default: module.AnalysisChart })),
);
class ChartBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
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
export function ResultChart(props: AnalysisChartProps) {
  return (
    <ChartBoundary>
      <Suspense fallback={<LoadingPanel label="正在加载图表" />}>
        <Chart {...props} />
      </Suspense>
    </ChartBoundary>
  );
}
