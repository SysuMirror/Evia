import type { DashboardDefinition, DashboardWidget } from '@evia/api-client';
import { parameterIssues } from './parameter-model';
export function initialDashboardDefinition(): DashboardDefinition {
  return { title: '', widgets: [] };
}
export function dashboardIssues(value: DashboardDefinition): string[] {
  const issues: string[] = [];
  if (!value.title.trim() || [...value.title.trim()].length > 128)
    issues.push('看板标题需为 1 至 128 个字符。');
  if (value.widgets.length < 1 || value.widgets.length > 12)
    issues.push('每个看板需有 1 至 12 个组件。');
  for (const widget of value.widgets) {
    if (!widget.title.trim() || [...widget.title.trim()].length > 128)
      issues.push('组件标题需为 1 至 128 个字符。');
    // Client-side mirror of the server/DB exclusive-source invariant: a widget
    // pins exactly one immutable source, never both and never neither.
    const hasAnalysis = widget.analysis_id !== undefined && widget.analysis_version !== undefined,
      hasMetric = !!widget.metric_ref;
    if (hasAnalysis === hasMetric)
      issues.push(`组件 ${widget.title || widget.widget_id} 必须且只能固定一个分析或指标来源。`);
  }
  if ([...(value.description ?? '')].length > 2000) issues.push('描述最多 2000 个字符。');
  issues.push(...parameterIssues(value));
  return issues;
}
export function availableWidgetPosition(widgets: readonly DashboardWidget[]) {
  const height = 5,
    width = 6;
  const bottom = Math.max(0, ...widgets.map((w) => w.layout.y + w.layout.height));
  for (let y = 0; y <= bottom; y++) {
    for (const x of [0, 6]) {
      if (
        widgets.every(
          (w) =>
            x + width <= w.layout.x ||
            w.layout.x + w.layout.width <= x ||
            y + height <= w.layout.y ||
            w.layout.y + w.layout.height <= y,
        )
      )
        return { x, y, width, height };
    }
  }
  return { x: 0, y: bottom, width, height };
}
/** Human-readable pinned-source label used by editor cards and run summaries. */
export function widgetSourceLabel(widget: DashboardWidget): string {
  if (widget.metric_ref) return `指标 v${widget.metric_ref.version}`;
  return `分析 v${widget.analysis_version ?? '—'}`;
}
export function reorderWidget(
  widgets: readonly DashboardWidget[],
  id: string,
  delta: number,
): DashboardWidget[] {
  const ordered = [...widgets].sort((a, b) => a.reading_order - b.reading_order);
  const at = ordered.findIndex((w) => w.widget_id === id),
    next = at + delta;
  if (at < 0 || next < 0 || next >= ordered.length) return [...widgets];
  [ordered[at], ordered[next]] = [ordered[next]!, ordered[at]!];
  return ordered.map((w, reading_order) => ({ ...w, reading_order }));
}
