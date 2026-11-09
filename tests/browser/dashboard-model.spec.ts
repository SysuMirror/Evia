import { expect, test } from '@playwright/test';
import type { DashboardDefinition, DashboardWidget } from '../../packages/api-client/src/index';
import {
  availableWidgetPosition,
  dashboardIssues,
  reorderWidget,
} from '../../apps/console/src/dashboards/model';
const widget: DashboardWidget = {
  widget_id: 'stable',
  analysis_id: 'analysis',
  analysis_version: 1,
  title: '组件',
  reading_order: 0,
  layout: { x: 0, y: 0, width: 6, height: 5 },
};
test('dashboard titles follow Unicode contract and require real widgets', () => {
  const definition: DashboardDefinition = { title: '😀'.repeat(128), widgets: [widget] };
  expect(dashboardIssues(definition)).toEqual([]);
  expect(dashboardIssues({ ...definition, title: '😀'.repeat(129) })).toContain(
    '看板标题需为 1 至 128 个字符。',
  );
  expect(dashboardIssues({ ...definition, widgets: [] })).toContain(
    '每个看板需有 1 至 12 个组件。',
  );
});
test('new widgets find clear adjacent space and preserve existing geometry', () => {
  const before = structuredClone(widget);
  expect(availableWidgetPosition([widget])).toEqual({ x: 6, y: 0, width: 6, height: 5 });
  expect(widget).toEqual(before);
  expect(
    availableWidgetPosition([
      widget,
      {
        ...widget,
        widget_id: 'two',
        reading_order: 1,
        layout: { x: 6, y: 0, width: 6, height: 5 },
      },
    ]),
  ).toEqual({ x: 0, y: 5, width: 6, height: 5 });
});
test('reading order changes preserve widget identity, version and coordinates', () => {
  const second = {
      ...widget,
      widget_id: 'second',
      reading_order: 1,
      analysis_version: 2,
      layout: { x: 6, y: 0, width: 6, height: 5 },
    },
    input = [widget, second];
  const reordered = reorderWidget(input, 'stable', 1);
  expect(reordered.map((w) => w.widget_id)).toEqual(['second', 'stable']);
  expect(reordered.map((w) => w.reading_order)).toEqual([0, 1]);
  expect(reordered[0]!.layout).toEqual(second.layout);
  expect(reordered[0]!.analysis_version).toBe(2);
  expect(input[0]!.reading_order).toBe(0);
  expect(reorderWidget(input, 'stable', -1)).toEqual(input);
});

test('delayed result completion cannot undo a confirmed child cancellation', async () => {
  const { retainDashboardProgress } =
    await import('../../apps/console/src/dashboards/run-progress');
  const base = {
    run_id: 'run',
    state: 'running',
    widgets: [
      { widget_id: 'a', query: { query_id: 'qa', state: 'succeeded' } },
      { widget_id: 'b', query: { query_id: 'qb', state: 'running' } },
    ],
  } as unknown as import('../../packages/api-client/src/index').DashboardRun;
  const canceled = structuredClone(base);
  canceled.widgets[1]!.query.state = 'canceled';
  canceled.state = 'partial';
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  let shown = canceled;
  const delayed = pending.then(() => {
    shown = retainDashboardProgress(shown, base);
  });
  release();
  await delayed;
  expect(shown.widgets[1]!.query.state).toBe('canceled');
  expect(shown.state).toBe('partial');
  const expired = structuredClone(shown);
  expired.widgets[0]!.query.state = 'expired';
  expired.state = 'failed';
  expect(retainDashboardProgress(shown, expired).widgets[0]!.query.state).toBe('expired');
});
