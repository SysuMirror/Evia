import { expect, test } from '@playwright/test';
import type { DashboardDefinition, DashboardWidget } from '../../packages/api-client/src/index';
import {
  availableWidgetPosition,
  buildRebindings,
  dashboardIssues,
  executableWidgets,
  maxDashboardSearchRunes,
  maxTextCardContentRunes,
  normalizeDashboardSearch,
  reorderWidget,
  widgetSourceLabel,
  type CrossCopySelection,
} from '../../apps/console/src/dashboards/model';
import { parseTextCardBlocks } from '../../apps/console/src/dashboards/text-card';
const widget: DashboardWidget = {
  widget_id: 'stable',
  analysis_id: 'analysis',
  analysis_version: 1,
  title: '组件',
  reading_order: 0,
  layout: { x: 0, y: 0, width: 6, height: 5 },
};
test('dashboard search terms trim blanks and clamp to the server rune limit', () => {
  expect(normalizeDashboardSearch('  营收 看板 ')).toBe('营收 看板');
  expect(normalizeDashboardSearch('   ')).toBe('');
  expect(normalizeDashboardSearch('')).toBe('');
  const atLimit = '字'.repeat(maxDashboardSearchRunes);
  expect(normalizeDashboardSearch(atLimit)).toBe(atLimit);
  const overLimit = '字'.repeat(maxDashboardSearchRunes + 1);
  const clamped = normalizeDashboardSearch(overLimit);
  expect([...clamped].length).toBe(maxDashboardSearchRunes);
  expect(clamped).toBe(atLimit);
  // Code points, not UTF-16 code units, define the limit and are preserved.
  const emojiTerm = ` ${'😀'.repeat(maxDashboardSearchRunes + 1)} `;
  const emojiClamped = normalizeDashboardSearch(emojiTerm);
  expect([...emojiClamped].length).toBe(maxDashboardSearchRunes);
  expect(emojiClamped.startsWith('😀')).toBe(true);
  // Inner whitespace is lexical data and must survive normalization.
  expect(normalizeDashboardSearch('a\tb  c')).toBe('a\tb  c');
});

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

const textWidget: DashboardWidget = {
  widget_id: 'text-card',
  title: '说明卡',
  reading_order: 0,
  layout: { x: 0, y: 0, width: 12, height: 3 },
  text_card: { content: '# 使用说明\n\n- 第一步\n- 第二步' },
};

test('text card widgets satisfy the three-way exclusive source invariant', () => {
  const valid: DashboardDefinition = { title: '文本看板', widgets: [textWidget] };
  expect(dashboardIssues(valid)).toEqual([]);
  expect(widgetSourceLabel(textWidget)).toBe('文本卡');

  const noSource = { ...valid, widgets: [{ ...textWidget, text_card: undefined }] };
  expect(dashboardIssues(noSource).join('\n')).toContain('必须且只能固定分析、指标或文本卡');

  const twoSources: DashboardDefinition = {
    title: '冲突看板',
    widgets: [{ ...textWidget, analysis_id: 'analysis', analysis_version: 1 }],
  };
  expect(dashboardIssues(twoSources).join('\n')).toContain('必须且只能固定分析、指标或文本卡');

  const emptyContent: DashboardDefinition = {
    title: '空文本',
    widgets: [{ ...textWidget, text_card: { content: '' } }],
  };
  expect(dashboardIssues(emptyContent).join('\n')).toContain('文本卡内容需为 1 至 4096 个字符');

  const overlong: DashboardDefinition = {
    title: '超长文本',
    widgets: [{ ...textWidget, text_card: { content: '字'.repeat(maxTextCardContentRunes + 1) } }],
  };
  expect(dashboardIssues(overlong).join('\n')).toContain('文本卡内容需为 1 至 4096 个字符');

  const withBinding: DashboardDefinition = {
    title: '带绑定文本',
    widgets: [
      {
        ...textWidget,
        parameter_bindings: [{ parameter_id: 'p', field: { kind: 'system', key: 'uid' } }],
      },
    ],
  };
  expect(dashboardIssues(withBinding).join('\n')).toContain('文本卡，不能绑定运行参数');
});

test('markdown subset parser keeps block structure and degrades unknown markers to text', () => {
  const blocks = parseTextCardBlocks(
    [
      '# 标题',
      '',
      '普通段落，',
      '软换行继续。',
      '',
      '> 引用一行',
      '',
      '- 无序 A',
      '- 无序 B',
      '',
      '1. 有序 A',
      '2. 有序 B',
      '',
      '```',
      'code line',
      '```',
    ].join('\n'),
  );
  expect(blocks.map((b) => b.kind)).toEqual([
    'heading',
    'paragraph',
    'quote',
    'list',
    'list',
    'code',
  ]);
  expect(blocks[0]).toMatchObject({ kind: 'heading', level: 1, text: '标题' });
  expect(blocks[1]).toMatchObject({ kind: 'paragraph', lines: ['普通段落，', '软换行继续。'] });
  expect(blocks[3]).toMatchObject({ kind: 'list', ordered: false, items: ['无序 A', '无序 B'] });
  expect(blocks[4]).toMatchObject({ kind: 'list', ordered: true, items: ['有序 A', '有序 B'] });
  expect(blocks[5]).toMatchObject({ kind: 'code', lines: ['code line'] });

  // An unterminated fence degrades defensively: remaining lines stay inside code.
  const unclosed = parseTextCardBlocks('正文\n\n```\nnever closed');
  expect(unclosed.at(-1)).toMatchObject({ kind: 'code', lines: ['never closed'] });
});

test('cross-environment rebindings cover every executable widget by source kind', () => {
  const analysisWidget: DashboardWidget = {
    widget_id: 'analysis-card',
    analysis_id: 'source-analysis',
    analysis_version: 1,
    title: '分析卡',
    reading_order: 0,
    layout: { x: 0, y: 0, width: 6, height: 4 },
  };
  const metricWidget: DashboardWidget = {
    widget_id: 'metric-card',
    metric_ref: { metric_id: 'source-metric', version: 2 },
    title: '指标卡',
    reading_order: 1,
    layout: { x: 6, y: 0, width: 6, height: 4 },
  };
  const textWidget: DashboardWidget = {
    widget_id: 'text-card',
    text_card: { content: '# 说明' },
    title: '文本卡',
    reading_order: 2,
    layout: { x: 0, y: 4, width: 12, height: 3 },
  };
  const widgets = [analysisWidget, metricWidget, textWidget];
  expect(executableWidgets(widgets).map((w) => w.widget_id)).toEqual([
    'analysis-card',
    'metric-card',
  ]);

  const selections: Record<string, CrossCopySelection> = {
    'analysis-card': { resourceID: 'target-analysis', version: '4' },
    'metric-card': { resourceID: 'target-metric', version: '5' },
  };
  expect(buildRebindings(widgets, selections)).toEqual([
    { widget_id: 'analysis-card', analysis_id: 'target-analysis', analysis_version: 4 },
    { widget_id: 'metric-card', metric_id: 'target-metric', metric_version: 5 },
  ]);

  // A text-only board has no executable widgets, so an empty selection is complete.
  expect(buildRebindings([textWidget], {})).toEqual([]);

  // Incomplete selections keep the submit action disabled rather than emit a partial body.
  expect(buildRebindings(widgets, { 'analysis-card': selections['analysis-card']! })).toBeNull();
  expect(
    buildRebindings(widgets, {
      ...selections,
      'metric-card': { resourceID: 'target-metric', version: '' },
    }),
  ).toBeNull();
});
