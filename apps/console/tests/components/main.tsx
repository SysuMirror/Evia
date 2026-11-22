import { StrictMode, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import { createRoot } from 'react-dom/client';
import { createMemoryRouter, Link, RouterProvider, useLocation } from 'react-router-dom';
import {
  Badge,
  Button,
  Checkbox,
  CopyButton,
  DashboardCard,
  DashboardGrid,
  DataSurface,
  DesignProvider,
  DataTable,
  Field,
  Input,
  Modal,
  PageHeading,
  Panel,
  Select,
  Textarea,
  Tabs,
  Toolbar,
  WorkspaceShell,
  useToast,
} from '@evia/design-system';
import { DraftNavigationProvider, useDraftNavigation } from '../../src/draft-navigation';
import { useUnsavedWork } from '../../src/use-unsaved-work';
import { SavedAnalysisChart, type SavedAnalysisChartProps } from '@evia/design-system/charts';

const chartFrom = Date.UTC(2026, 10, 9);
const chartHour = 3600000;
const chartTimeRange = { from: chartFrom, to: chartFrom + 5 * chartHour };
const partialChartTimeRange = { from: chartFrom + 11 * chartHour, to: chartFrom + 12 * chartHour };
const partialChartCategories = [
  { key: 'day', label: new Date(chartFrom).toISOString(), timestamp: chartFrom },
];
const partialChartSeries = [{ id: 'count', label: '事件数', unit: '次', values: [3] }];
const chartCategories = [1, 2, 4].map((hour) => ({
  key: String(hour),
  label: new Date(chartFrom + hour * chartHour).toISOString(),
  timestamp: chartFrom + hour * chartHour,
}));
const chartSeries = [
  { id: 'count-missing', label: '事件数，渠道缺失', unit: '次', values: [2, 4, 6] },
  { id: 'average-null', label: '平均金额，渠道 null', values: [1, Infinity, null] },
];
const groupedChartCategories = [
  { key: 'missing', label: '渠道缺失' },
  { key: 'null', label: '渠道 null' },
  { key: 'empty', label: '渠道为空字符串' },
];
const groupedChartSeries = [
  { id: 'count', label: '事件数', unit: '次', values: [2, null, 4] },
  { id: 'sum', label: '金额总和', values: [-1, 0, 3] },
];

function SavedChartFixture() {
  const [kind, setKind] = useState<SavedAnalysisChartProps['kind']>('line');
  const [mounted, setMounted] = useState(true);
  const [snapshot, setSnapshot] = useState('');
  const [partial, setPartial] = useState(false);
  const grouped = kind !== 'line';
  const categories = partial
    ? partialChartCategories
    : grouped
      ? groupedChartCategories
      : chartCategories;
  const series = partial ? partialChartSeries : grouped ? groupedChartSeries : chartSeries;
  return (
    <section aria-label="保存分析图表组件" className="section-stack" id="saved-chart-fixture">
      <h2>保存分析图表</h2>
      <div className="inline-actions">
        <Button
          onClick={() => {
            setPartial(false);
            setKind('line');
          }}
        >
          显示时间折线图
        </Button>
        <Button
          onClick={() => {
            setPartial(false);
            setKind('bar');
          }}
        >
          显示分组柱状图
        </Button>
        <Button
          onClick={() => {
            setPartial(false);
            setKind('funnel');
          }}
        >
          显示漏斗图
        </Button>
        <Button
          onClick={() => {
            setPartial(true);
            setKind('line');
          }}
        >
          显示局部首桶
        </Button>
        <Button onClick={() => setMounted((value) => !value)}>
          {mounted ? '卸载图表' : '挂载图表'}
        </Button>
        <Button
          onClick={async () => {
            setSnapshot('');
            const element = document.querySelector<HTMLElement>(
              '#saved-chart-fixture .saved-analysis-chart',
            );
            // Inspect the renderer module actually loaded by Vite. Importing a
            // second copy by filesystem path creates a separate instance registry.
            const coreURL = performance
              .getEntriesByType('resource')
              .find((entry) => /\/echarts_core\.js(?:\?|$)/.test(entry.name))?.name;
            if (!coreURL || !element) return;
            const core = (await import(/* @vite-ignore */ coreURL)) as {
              getInstanceByDom: (node: HTMLElement) => { getOption: () => unknown } | undefined;
            };
            const chart = core.getInstanceByDom(element);
            if (chart) setSnapshot(JSON.stringify(chart.getOption()));
          }}
        >
          读取图表状态
        </Button>
      </div>
      {mounted && (
        <SavedAnalysisChart
          kind={kind}
          label="合成图表测试"
          timezone="Asia/Shanghai"
          categories={categories}
          series={series}
          timeRange={partial ? partialChartTimeRange : chartTimeRange}
          intervalMs={partial ? 24 * chartHour : chartHour}
        />
      )}
      <DataTable
        caption="合成图表完整结果"
        rows={categories.map((category, index) => ({
          key: category.key,
          label: category.label,
          values: series.map((item) => item.values[index]),
        }))}
        rowKey={(row) => row.key}
        minWidth={400}
        columns={[
          { key: 'category', header: '时间或分组', render: (row) => row.label },
          ...series.map((item, index) => ({
            key: item.id,
            header: item.label,
            render: (row: { values: readonly (number | null | undefined)[] }) =>
              row.values[index] === null || row.values[index] === undefined
                ? '缺失'
                : Number.isFinite(row.values[index])
                  ? String(row.values[index])
                  : '非有限值（未绘制）',
          })),
        ]}
      />
      <output
        className="code-block"
        aria-label="图表实际配置"
        style={{ display: 'block', maxHeight: 180 }}
      >
        {snapshot}
      </output>
      <p className="field-help">
        本隔离组件使用合成数据，测试两个指标、缺失分组、null、零值、非有限数值与缺失时间桶。
      </p>
    </section>
  );
}

function GuardFixture() {
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [leaves, setLeaves] = useState(0);
  const navigation = useDraftNavigation();
  const location = useLocation();
  const { requestLeave, confirmation } = useUnsavedWork({
    dirty: draft !== '',
    busy,
    onDiscard: () => setDraft(''),
  });
  return (
    <>
      <Field label="未保存内容" htmlFor="fixture-draft">
        <Input value={draft} onChange={(event) => setDraft(event.target.value)} disabled={busy} />
      </Field>
      <div className="inline-actions">
        <Button onClick={() => requestLeave(() => setLeaves((count) => count + 1))}>
          离开编辑
        </Button>
        <Button onClick={() => navigation.request(() => setLeaves((count) => count + 1))}>
          请求全局离开
        </Button>
        <Button onClick={() => setBusy((value) => !value)}>
          {busy ? '完成测试操作' : '开始测试操作'}
        </Button>
        <Link to="/other">切换测试页面</Link>
        <Link to="/">返回测试页面</Link>
      </div>
      <output aria-label="离开次数">{leaves}</output>{' '}
      <output aria-label="测试路径">{location.pathname}</output>{' '}
      <output aria-label="测试操作状态">{busy ? '处理中' : '空闲'}</output>
      {confirmation}
    </>
  );
}

const guardRouter = createMemoryRouter([
  {
    path: '*',
    element: (
      <DraftNavigationProvider>
        <GuardFixture />
      </DraftNavigationProvider>
    ),
  },
]);

function LifecycleFixture() {
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  const [nested, setNested] = useState(false);
  const [modalBusy, setModalBusy] = useState(false);
  const [triggerVisible, setTriggerVisible] = useState(true);
  const [invalid, setInvalid] = useState(false);
  const [validSubmissions, setValidSubmissions] = useState(0);
  const nextFocus = useRef<HTMLInputElement>(null);
  const safeFocus = useRef<HTMLButtonElement>(null);
  return (
    <section aria-label="控件生命周期" className="section-stack">
      <div className="inline-actions">
        <Button loading={busy} onClick={() => setBusy(true)}>
          执行异步操作
        </Button>
        <Button onClick={() => setBusy(false)}>完成异步操作</Button>
        {triggerVisible && <Button onClick={() => setOpen(true)}>打开测试对话框</Button>}
      </div>
      <Field label="完成后继续编辑" htmlFor="fixture-next-focus">
        <Input ref={nextFocus} />
      </Field>
      <form
        aria-label="必填校验表单"
        onSubmit={(event) => {
          event.preventDefault();
          setValidSubmissions((value) => value + 1);
        }}
      >
        <Field
          label="必填名称"
          htmlFor="fixture-required"
          required
          hint="名称用于区分查询。"
          error={invalid ? '请填写名称' : undefined}
        >
          <Input
            onInvalid={() => setInvalid(true)}
            onChange={(event) => setInvalid(!event.currentTarget.validity.valid)}
          />
        </Field>
        <Button type="submit">提交必填表单</Button>
      </form>
      <output aria-label="校验成功次数">{validSubmissions}</output>
      <Modal
        open={open}
        title="测试对话框"
        onRequestClose={() => setOpen(false)}
        busy={modalBusy}
        initialFocusRef={safeFocus}
        footer={
          <Button ref={safeFocus} onClick={() => setOpen(false)} disabled={modalBusy}>
            安全返回
          </Button>
        }
      >
        <div className="section-stack">
          <Field label="对话框内容" htmlFor="fixture-modal-input">
            <Input />
          </Field>
          <div className="inline-actions">
            <Button onClick={() => setNested(true)}>打开嵌套确认</Button>
            <Button onClick={() => setModalBusy((value) => !value)}>
              {modalBusy ? '结束对话框操作' : '开始对话框操作'}
            </Button>
            <Button
              onClick={() => {
                flushSync(() => setOpen(false));
                nextFocus.current?.focus();
              }}
            >
              关闭并继续编辑
            </Button>
            <Button
              onClick={() => {
                setTriggerVisible(false);
                setOpen(false);
              }}
            >
              关闭并移除入口
            </Button>
          </div>
        </div>
      </Modal>
      <Modal
        open={nested}
        title="嵌套确认"
        onRequestClose={() => setNested(false)}
        footer={<Button onClick={() => setNested(false)}>取消嵌套确认</Button>}
      >
        <p>仅关闭当前确认。</p>
      </Modal>
    </section>
  );
}

function WorkspacePreviewFixture() {
  const navigation = [
    ['事件管理', '/events'],
    ['发布管理', '/releases'],
    ['接入来源', '/sources'],
    ['数据分析', '/analysis'],
    ['保存的分析', '/saved-analyses'],
    ['自定义看板', '/dashboards'],
    ['会话回放', '/session-replay'],
    ['项目治理', '/governance'],
  ] as const;
  return (
    <WorkspaceShell
      account="测试管理员"
      busy={false}
      onLogout={() => undefined}
      navigation={navigation.map(([label, href]) => ({
        label,
        href,
        active: href === '/dashboards',
      }))}
      projectContext={
        <div className="workspace-project-context">
          <Button variant="link" className="workspace-project-back">
            <span aria-hidden="true">←</span>
            返回项目列表
          </Button>
          <strong className="workspace-project-name">产品体验分析</strong>
          <Field
            className="workspace-project-environment"
            label="当前环境"
            htmlFor="preview-environment"
          >
            <Select
              id="preview-environment"
              defaultValue="production"
              options={[
                { value: 'development', label: '开发环境' },
                { value: 'production', label: '生产环境' },
              ]}
            />
          </Field>
        </div>
      }
    >
      <div className="dashboard-workspace section-stack">
        <PageHeading
          title="运营概览"
          action={
            <>
              <Button>查看版本</Button>
              <Button variant="primary">运行看板</Button>
            </>
          }
        />
        <Panel className="dashboard-run-controls" label="看板执行条件">
          <Toolbar actions={<Button>调整条件</Button>}>
            <strong>最近 7 天</strong>
            <span className="field-help">生产环境，Asia/Shanghai</span>
          </Toolbar>
        </Panel>
        <DashboardGrid
          label="合成看板布局预览"
          items={[
            {
              id: 'sessions',
              reading_order: 1,
              layout: { x: 0, y: 0, width: 4, height: 2 },
              content: (
                <DashboardCard title="访问会话" summary="较前 7 天">
                  <div className="analysis-stat">
                    <div className="metric-value">12,480</div>
                    <span>增长 8.2%</span>
                  </div>
                </DashboardCard>
              ),
            },
            {
              id: 'trend',
              reading_order: 2,
              layout: { x: 4, y: 0, width: 8, height: 5 },
              content: (
                <DashboardCard
                  title="事件趋势"
                  summary="2026/11/09 00:00 至 05:00"
                  action={<Badge tone="success">已完成</Badge>}
                >
                  <SavedAnalysisChart
                    kind="line"
                    label="事件趋势组件预览"
                    timezone="Asia/Shanghai"
                    categories={chartCategories}
                    series={chartSeries.slice(0, 1)}
                    timeRange={chartTimeRange}
                    intervalMs={chartHour}
                  />
                </DashboardCard>
              ),
            },
            {
              id: 'routes',
              reading_order: 3,
              layout: { x: 0, y: 2, width: 4, height: 3 },
              content: (
                <DashboardCard title="热门路径" summary="按访问次数排序">
                  <DataSurface label="热门路径结果">
                    <DataTable
                      caption="热门路径结果"
                      minWidth={320}
                      rows={[
                        { route: '/home', count: 4_820 },
                        { route: '/search', count: 2_140 },
                        { route: '/detail', count: 1_390 },
                      ]}
                      rowKey={(row) => row.route}
                      columns={[
                        { key: 'route', header: '路径', render: (row) => row.route },
                        {
                          key: 'count',
                          header: '访问次数',
                          align: 'right',
                          render: (row) => row.count.toLocaleString('zh-CN'),
                        },
                      ]}
                    />
                  </DataSurface>
                </DashboardCard>
              ),
            },
          ]}
        />
      </div>
    </WorkspaceShell>
  );
}

// Isolated development fixture. This HTML entry is not a product route or a build input.
function ComponentFixture() {
  const toast = useToast();
  const [environment, setEnvironment] = useState('development');
  const [notes, setNotes] = useState('初始说明');
  const [enabled, setEnabled] = useState(false);
  const [lastFocus, setLastFocus] = useState('无');
  const [submissions, setSubmissions] = useState(0);
  const [clicks, setClicks] = useState(0);
  const [disabledClicks, setDisabledClicks] = useState(0);
  const [pressed, setPressed] = useState(false);
  const [copyValue, setCopyValue] = useState('值 A');
  const [copyMounted, setCopyMounted] = useState(true);
  const [tab, setTab] = useState('overview');
  const [tabChanges, setTabChanges] = useState(0);

  return (
    <main tabIndex={-1} style={{ maxWidth: 800, margin: '24px auto', padding: 24 }}>
      <h1>组件交互测试</h1>
      <section aria-labelledby="native-title">
        <h2 id="native-title">原生控件</h2>
        <form
          aria-label="控件表单"
          onSubmit={(event) => {
            event.preventDefault();
            setSubmissions((value) => value + 1);
          }}
        >
          <Field label="环境" htmlFor="fixture-environment" hint="使用方向键选择环境。">
            <Select
              id="fixture-environment"
              value={environment}
              onFocus={() => setLastFocus('环境')}
              onChange={(event) => setEnvironment(event.target.value)}
              options={[
                { value: 'development', label: '开发' },
                { value: 'testing', label: '测试' },
                { value: 'retired', label: '已停用', disabled: true },
                { value: 'production', label: '生产' },
              ]}
            />
          </Field>
          <Field label="说明" htmlFor="fixture-notes">
            <Textarea
              id="fixture-notes"
              value={notes}
              onChange={(event) => setNotes(event.target.value)}
            />
          </Field>
          <Checkbox checked={enabled} onChange={(event) => setEnabled(event.target.checked)}>
            启用记录
          </Checkbox>
          <Field label="只读编号" htmlFor="fixture-readonly-input">
            <Input id="fixture-readonly-input" readOnly value="只读编号 123" />
          </Field>
          <Field label="只读说明" htmlFor="fixture-readonly-textarea">
            <Textarea id="fixture-readonly-textarea" readOnly value={'第一行\n第二行'} />
          </Field>
          <Field label="禁用输入" htmlFor="fixture-disabled-input">
            <Input id="fixture-disabled-input" disabled value="不能修改" />
          </Field>
          <Field label="禁用多行输入" htmlFor="fixture-disabled-textarea">
            <Textarea id="fixture-disabled-textarea" disabled value="不能修改多行内容" />
          </Field>
          <Field label="禁用且只读输入" htmlFor="fixture-disabled-readonly-input">
            <Input id="fixture-disabled-readonly-input" disabled readOnly value="禁用且只读" />
          </Field>
          <Field label="禁用且只读多行输入" htmlFor="fixture-disabled-readonly-textarea">
            <Textarea
              id="fixture-disabled-readonly-textarea"
              disabled
              readOnly
              value="禁用且只读多行内容"
            />
          </Field>
          <Checkbox disabled onChange={() => setDisabledClicks((value) => value + 1)}>
            禁用选项
          </Checkbox>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginBlock: 16 }}>
            <Button
              type="submit"
              onClick={(event) => {
                event.preventDefault();
                setClicks((value) => value + 1);
              }}
            >
              阻止提交
            </Button>
            <Button type="submit">提交表单</Button>
            <Button disabled onClick={() => setDisabledClicks((value) => value + 1)}>
              禁用操作
            </Button>
          </div>
        </form>
        <output aria-label="选择结果">{environment}</output>{' '}
        <output aria-label="说明结果">{notes}</output>{' '}
        <output aria-label="勾选结果">{enabled ? '已启用' : '未启用'}</output>{' '}
        <output aria-label="焦点事件">{lastFocus}</output>{' '}
        <output aria-label="提交次数">{submissions}</output>{' '}
        <output aria-label="点击次数">{clicks}</output>{' '}
        <output aria-label="禁用激活次数">{disabledClicks}</output>
      </section>
      <section aria-labelledby="button-title">
        <h2 id="button-title">按钮状态</h2>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          <Button variant="primary">主要按钮</Button>
          <Button variant="secondary">次要按钮</Button>
          <Button variant="danger">危险按钮</Button>
          <Button variant="ghost">幽灵按钮</Button>
          <Button variant="link">链接按钮</Button>
          <Button aria-pressed={pressed} onClick={() => setPressed((value) => !value)}>
            切换选择
          </Button>
        </div>
      </section>
      <section aria-labelledby="copy-title">
        <h2 id="copy-title">异步复制</h2>
        <Field label="待复制值" htmlFor="fixture-copy-value">
          <Input
            id="fixture-copy-value"
            value={copyValue}
            onChange={(event) => setCopyValue(event.target.value)}
          />
        </Field>
        <Button onClick={() => setCopyMounted((value) => !value)}>
          {copyMounted ? '卸载复制控件' : '挂载复制控件'}
        </Button>
        {copyMounted && <CopyButton value={copyValue} label="复制测试值" />}
      </section>
      <section aria-labelledby="toast-title">
        <h2 id="toast-title">通知生命周期</h2>
        <Button onClick={() => toast.success('测试成功通知')}>显示成功通知</Button>{' '}
        <Button onClick={() => toast.error('测试错误通知')}>显示错误通知</Button>
        <Button
          onClick={() => {
            for (let index = 1; index <= 6; index++) toast.success(`新增通知 ${index}`);
          }}
        >
          批量显示通知
        </Button>
      </section>
      <section aria-labelledby="tabs-title">
        <h2 id="tabs-title">面板切换</h2>
        <Tabs
          label="事件内容"
          value={tab}
          onChange={(value) => {
            setTab(value);
            setTabChanges((count) => count + 1);
          }}
          items={[
            { id: 'overview', label: '概览', content: <p>事件概览内容</p> },
            { id: 'disabled', label: '停用页', content: <p>不可访问内容</p>, disabled: true },
            {
              id: 'properties',
              label: '属性',
              content: (
                <Field label="面板备注" htmlFor="fixture-tab-note">
                  <Input defaultValue="保留内容" />
                </Field>
              ),
            },
          ]}
        />
        <output aria-label="面板切换次数">{tabChanges}</output>
      </section>
      <section aria-labelledby="guard-title">
        <h2 id="guard-title">未保存保护</h2>
        <RouterProvider router={guardRouter} />
      </section>
      <LifecycleFixture />
      <SavedChartFixture />
    </main>
  );
}

const previewFixture = new URLSearchParams(window.location.search).get('fixture') === 'workspace';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <DesignProvider>
      {previewFixture ? <WorkspacePreviewFixture /> : <ComponentFixture />}
    </DesignProvider>
  </StrictMode>,
);
