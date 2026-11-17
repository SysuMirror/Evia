import type { DashboardDefinition, DashboardRebinding, DashboardWidget } from '@evia/api-client';
import { parameterIssues } from './parameter-model';

/** Mirrors the server-side reporting.MaxTextCardContentRunes boundary. */
export const maxTextCardContentRunes = 4096;

/** Mirrors the server-side reporting.maxDashboardSearchRunes boundary. */
export const maxDashboardSearchRunes = 128;

/**
 * Normalizes the title search box before the term reaches the API: trims
 * surrounding whitespace and clamps to the server rune limit so an over-long
 * local input can never trigger a guaranteed 400. A blank result means "no
 * filter", which the history hook encodes by omitting the q parameter.
 */
export function normalizeDashboardSearch(input: string): string {
  const trimmed = input.trim();
  const runes = [...trimmed];
  return runes.length > maxDashboardSearchRunes
    ? runes.slice(0, maxDashboardSearchRunes).join('')
    : trimmed;
}

/** Per-widget target selection captured by the cross-environment copy form. */
export type CrossCopySelection = { resourceID: string; version: string };

/** Executable widgets only: presentation text cards need no rebinding. */
export function executableWidgets(widgets: readonly DashboardWidget[]): DashboardWidget[] {
  return widgets.filter((widget) => !widget.text_card);
}

/**
 * Builds the rebinding payload from the form's selections. Returns null when
 * any executable widget lacks a complete resource+version selection, so the
 * caller can keep the submit action disabled rather than send a partial body.
 */
export function buildRebindings(
  widgets: readonly DashboardWidget[],
  selections: Record<string, CrossCopySelection>,
): DashboardRebinding[] | null {
  const executable = executableWidgets(widgets);
  if (
    !executable.every(
      (widget) =>
        !!selections[widget.widget_id]?.resourceID && !!selections[widget.widget_id]?.version,
    )
  ) {
    return null;
  }
  return executable.map((widget) => {
    const selected = selections[widget.widget_id]!;
    const version = Number(selected.version);
    return widget.metric_ref
      ? {
          widget_id: widget.widget_id,
          metric_id: selected.resourceID,
          metric_version: version,
        }
      : {
          widget_id: widget.widget_id,
          analysis_id: selected.resourceID,
          analysis_version: version,
        };
  });
}

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
    // pins exactly one source kind — an analysis version, a metric version or
    // a presentation-only text card — never several and never none.
    const hasAnalysis = widget.analysis_id !== undefined && widget.analysis_version !== undefined,
      hasMetric = !!widget.metric_ref,
      hasTextCard = !!widget.text_card;
    if ([hasAnalysis, hasMetric, hasTextCard].filter(Boolean).length !== 1)
      issues.push(
        `组件 ${widget.title || widget.widget_id} 必须且只能固定分析、指标或文本卡中的一种来源。`,
      );
    if (hasTextCard) {
      const contentLength = [...(widget.text_card?.content ?? '')].length;
      if (contentLength < 1 || contentLength > 4096)
        issues.push(`组件 ${widget.title || widget.widget_id} 的文本卡内容需为 1 至 4096 个字符。`);
      if ((widget.parameter_bindings ?? []).length > 0)
        issues.push(`组件 ${widget.title || widget.widget_id} 是文本卡，不能绑定运行参数。`);
    }
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
  if (widget.text_card) return '文本卡';
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
