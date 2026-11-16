import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type FormulaMetricRecord,
  type FormulaMetricValidation,
  type FormulaMetricVersion,
  type Project,
  type QueryJob,
  type QueryResult,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  ErrorPanel,
  Field,
  Input,
  LoadingPanel,
  Modal,
  PageHeading,
  PaginationBar,
  Panel,
  Select,
  Tabs,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import { useFormulaMetricsRequest } from './use-formula-metrics-request';
import {
  buildFormulaRun,
  defaultFormState,
  defaultRunForm,
  formatFormulaInstant,
  formulaOperators,
  formulaUnitLabels,
  formulaUnits,
  fromFormulaDefinition,
  operatorLabels,
  toFormulaDefinition,
  type FormulaFormState,
  type FormulaRunForm,
  type OperandState,
} from './model';
import './styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };
type FormulaMetricListView = 'active' | 'archived';
type FormulaOperandOption = { id: string; version: number; label: string };

export function FormulaMetricsPage({ project, session, environment }: PageProps) {
  const canRead = project.permissions.includes('metrics.read');
  const canWrite = project.permissions.includes('metrics.write');
  const canPublish = project.permissions.includes('metrics.publish');
  const canRun = project.permissions.includes('metrics.run');
  const toast = useToast();
  const queryClient = useQueryClient();
  const requests = useFormulaMetricsRequest(
    project,
    session,
    `${project.id}:${environment}:${session.user_id}:${session.csrf_token}`,
    canWrite || canPublish,
  );
  const [metricID, setMetricID] = useState<string>();
  const [creating, setCreating] = useState(false);
  const [tab, setTab] = useState('draft');
  const [runVersion, setRunVersion] = useState(0);
  const [cursors, setCursors] = useState<string[]>([]);
  const [archiveView, setArchiveView] = useState<FormulaMetricListView>('active');
  const [pendingArchive, setPendingArchive] = useState<FormulaMetricRecord | null>(null);
  const [archiveSubmitted, setArchiveSubmitted] = useState(false);

  const list = useQuery({
    queryKey: [
      'formula-metrics',
      project.id,
      session.user_id,
      environment,
      'list',
      archiveView,
      cursors.at(-1) ?? '',
    ],
    queryFn: ({ signal }) =>
      api.listFormulaMetrics(
        project.id,
        { environment, view: archiveView, limit: 20, cursor: cursors.at(-1) },
        signal,
      ),
    enabled: canRead && !metricID && !creating,
  });

  useEffect(() => {
    if (archiveSubmitted && requests.busy !== '复合指标归档') {
      setPendingArchive(null);
      setArchiveSubmitted(false);
    }
  }, [requests.busy, archiveSubmitted]);

  const invalidateList = () =>
    void queryClient.invalidateQueries({ queryKey: ['formula-metrics', project.id] });

  const archive = (metric: FormulaMetricRecord) => {
    void requests.perform(
      '复合指标归档',
      (signal) =>
        api.archiveFormulaMetric(project.id, metric.metric_id, session.csrf_token, signal),
      () => {
        toast.success(`复合指标「${metric.title}」已归档，可在已归档视图中恢复。`);
        invalidateList();
      },
    );
  };
  const unarchive = (metric: FormulaMetricRecord) => {
    void requests.perform(
      '复合指标恢复',
      (signal) =>
        api.unarchiveFormulaMetric(project.id, metric.metric_id, session.csrf_token, signal),
      () => {
        toast.success(`复合指标「${metric.title}」已恢复到活跃列表。`);
        invalidateList();
      },
    );
  };

  const current = useQuery({
    queryKey: ['formula-metrics', project.id, session.user_id, environment, metricID, 'summary'],
    queryFn: ({ signal }) => api.getFormulaMetric(project.id, metricID!, signal),
    enabled: !!metricID,
  });

  if (!canRead)
    return (
      <>
        <PageHeading title="复合公式指标" />
        <ErrorPanel message="你没有读取指标的权限。" />
      </>
    );
  if (requests.denied)
    return (
      <>
        <PageHeading title="复合公式指标" />
        <ErrorPanel message={requests.denied.message} />
      </>
    );

  const openMetric = (id: string) => {
    setMetricID(id);
    setCreating(false);
    setTab('draft');
  };

  if (creating)
    return (
      <div className="formula-metrics-workspace section-stack">
        <PageHeading
          title="新建复合公式指标"
          action={<Button onClick={() => setCreating(false)}>返回列表</Button>}
        />
        <FormulaEditor
          project={project}
          session={session}
          environment={environment}
          canWrite={canWrite}
          canPublish={canPublish}
          perform={requests.perform}
          busy={requests.busy}
          onCreated={openMetric}
        />
      </div>
    );

  if (metricID) {
    const published = current.data?.published_version ?? 0;
    return (
      <div className="formula-metrics-workspace section-stack">
        <PageHeading
          title={current.data?.title ?? '复合公式指标工作区'}
          action={<Button onClick={() => setMetricID(undefined)}>返回列表</Button>}
        />
        {current.isPending ? (
          <LoadingPanel label="正在加载复合指标" />
        ) : current.isError ? (
          <ErrorPanel
            message={current.error instanceof ApiError ? current.error.message : '复合指标读取失败'}
            retry={() => void current.refetch()}
          />
        ) : (
          <Panel className="formula-metrics-tabs">
            <Tabs
              label="复合指标分区"
              value={tab}
              onChange={setTab}
              items={[
                { id: 'draft', label: '草稿与发布', content: null },
                { id: 'versions', label: '已发布版本', content: null, disabled: published < 1 },
                { id: 'run', label: '运行', content: null, disabled: published < 1 },
              ].map((item) => ({
                ...item,
                content:
                  item.id === 'draft' ? (
                    <FormulaEditor
                      key={metricID}
                      project={project}
                      session={session}
                      environment={environment}
                      metricID={metricID}
                      canWrite={canWrite}
                      canPublish={canPublish}
                      perform={requests.perform}
                      busy={requests.busy}
                      onCreated={openMetric}
                    />
                  ) : item.id === 'versions' ? (
                    <FormulaVersionsPanel
                      project={project}
                      session={session}
                      metricID={metricID}
                      selectedVersion={runVersion || published}
                      onSelectVersion={(version) => {
                        setRunVersion(version);
                        setTab('run');
                      }}
                    />
                  ) : (
                    <FormulaRunPanel
                      project={project}
                      session={session}
                      metricID={metricID}
                      version={runVersion || published}
                      canRun={canRun}
                    />
                  ),
              }))}
            />
          </Panel>
        )}
      </div>
    );
  }

  const archivedView = archiveView === 'archived';
  const columns: DataTableColumn<FormulaMetricRecord>[] = [
    {
      key: 'title',
      header: '复合指标',
      render: (row) =>
        archivedView ? (
          <span>{row.title}</span>
        ) : (
          <Button variant="link" onClick={() => openMetric(row.metric_id)}>
            {row.title}
          </Button>
        ),
    },
    {
      key: 'published',
      header: '已发布版本',
      render: (row) => (
        <Badge tone={row.published_version ? 'success' : 'neutral'}>v{row.published_version}</Badge>
      ),
    },
    { key: 'draft', header: '草稿修订', render: (row) => row.draft_revision },
    {
      key: 'updated',
      header: archivedView ? '归档时间' : '更新时间',
      render: (row) => formatFormulaInstant(row.archived_at ?? row.updated_at),
    },
    {
      key: 'actions',
      header: '操作',
      render: (row) =>
        canWrite ? (
          archivedView ? (
            <Button size="small" disabled={!!requests.busy} onClick={() => unarchive(row)}>
              恢复指标
            </Button>
          ) : (
            <Button
              size="small"
              variant="danger"
              disabled={!!requests.busy}
              onClick={() => {
                setArchiveSubmitted(false);
                setPendingArchive(row);
              }}
            >
              归档
            </Button>
          )
        ) : (
          <span className="evia-field-hint">只读</span>
        ),
    },
  ];

  return (
    <div className="formula-metrics-workspace section-stack">
      <PageHeading
        title="复合公式指标"
        action={
          canWrite ? (
            <Button variant="primary" onClick={() => setCreating(true)} disabled={!!requests.busy}>
              新建复合指标
            </Button>
          ) : undefined
        }
      />
      <DataSurface label="复合指标列表">
        <div className="formula-view-switch" role="group" aria-label="复合指标视图切换">
          <Button
            size="small"
            variant={archivedView ? 'ghost' : 'secondary'}
            aria-pressed={!archivedView}
            disabled={!!requests.busy}
            onClick={() => {
              setArchiveView('active');
              setCursors([]);
            }}
          >
            活跃指标
          </Button>
          <Button
            size="small"
            variant={archivedView ? 'secondary' : 'ghost'}
            aria-pressed={archivedView}
            disabled={!!requests.busy}
            onClick={() => {
              setArchiveView('archived');
              setCursors([]);
            }}
          >
            已归档
          </Button>
        </div>
        {list.isPending ? (
          <LoadingPanel label="正在加载复合指标" />
        ) : list.isError ? (
          <ErrorPanel
            message={list.error instanceof ApiError ? list.error.message : '复合指标列表读取失败'}
            retry={() => void list.refetch()}
          />
        ) : (
          <>
            <DataTable
              caption={archivedView ? '当前项目和环境的已归档复合指标' : '复合指标列表'}
              columns={columns}
              rows={list.data?.items ?? []}
              rowKey={(row) => row.metric_id}
              minWidth={620}
              loading={list.isFetching}
              empty={
                <EmptyPanel
                  description={
                    archivedView
                      ? '暂无已归档复合指标；归档后的复合指标会移至此处，可随时恢复。'
                      : '还没有复合公式指标，新建一个来组合已发布的原子指标。'
                  }
                />
              }
            />
            <PaginationBar
              page={cursors.length + 1}
              count={list.data?.items.length ?? 0}
              hasNext={!!list.data?.next_cursor}
              busy={list.isFetching || !!requests.busy}
              onPrevious={() => setCursors((current) => current.slice(0, -1))}
              onNext={() => {
                if (list.data?.next_cursor)
                  setCursors((current) => [...current, list.data!.next_cursor!]);
              }}
            />
          </>
        )}
      </DataSurface>
      <Modal
        open={!!pendingArchive}
        title="确认归档复合指标"
        busy={requests.busy === '复合指标归档'}
        onRequestClose={() => {
          if (!requests.busy) setPendingArchive(null);
        }}
        footer={
          <>
            <Button disabled={!!requests.busy} onClick={() => setPendingArchive(null)}>
              取消
            </Button>
            <Button
              variant="danger"
              loading={requests.busy === '复合指标归档'}
              disabled={!pendingArchive || (!!requests.busy && requests.busy !== '复合指标归档')}
              onClick={() => {
                if (pendingArchive) {
                  setArchiveSubmitted(true);
                  archive(pendingArchive);
                }
              }}
            >
              确认归档
            </Button>
          </>
        }
      >
        <p>
          归档后，复合指标「<strong>{pendingArchive?.title}</strong>
          」将从默认列表隐藏，且草稿保存、校验、发布与运行入口全部关闭；已发布的不可变版本、原子依赖见证与审计记录保留，历史运行与保存分析对其版本的引用继续有效，可在「已归档」视图中恢复。
        </p>
      </Modal>
    </div>
  );
}

interface EditorProps {
  project: Project;
  session: Session;
  environment: Environment;
  metricID?: string;
  canWrite: boolean;
  canPublish: boolean;
  perform: ReturnType<typeof useFormulaMetricsRequest>['perform'];
  busy: string;
  onCreated: (id: string) => void;
}

function FormulaEditor({
  project,
  session,
  environment,
  metricID,
  canWrite,
  canPublish,
  perform,
  busy,
  onCreated,
}: EditorProps) {
  const draft = useQuery({
    queryKey: ['formula-metrics', project.id, session.user_id, environment, metricID, 'draft'],
    queryFn: ({ signal }) => api.getFormulaMetricDraft(project.id, metricID!, signal),
    enabled: !!metricID,
  });
  // Published atomic and composite metrics are both selectable operands.
  const atomic = useQuery({
    queryKey: ['metrics', project.id, session.user_id, environment, 'formula-operands'],
    queryFn: ({ signal }) => api.listMetrics(project.id, { environment, limit: 100 }, signal),
    enabled: canWrite,
  });
  const composite = useQuery({
    queryKey: ['formula-metrics', project.id, session.user_id, environment, 'formula-operands'],
    queryFn: ({ signal }) =>
      api.listFormulaMetrics(project.id, { environment, limit: 100 }, signal),
    enabled: canWrite,
  });
  const [form, setForm] = useState<FormulaFormState>(() => defaultFormState());
  const [loadedFor, setLoadedFor] = useState<string>();
  const [error, setError] = useState('');
  const [validation, setValidation] = useState<FormulaMetricValidation>();

  useEffect(() => {
    if (metricID && draft.data && loadedFor !== metricID) {
      setForm(fromFormulaDefinition(draft.data.definition));
      setLoadedFor(metricID);
    }
  }, [metricID, draft.data, loadedFor]);

  const operands = (atomic.data?.items ?? []).filter((row) => row.published_version > 0);
  const compositeOperands = (composite.data?.items ?? []).filter(
    (row) => row.published_version > 0 && row.metric_id !== metricID,
  );
  const operandOptions: FormulaOperandOption[] = [
    ...operands.map((row) => ({
      id: row.metric_id,
      version: row.published_version,
      label: `原子｜${row.title}（v${row.published_version}）`,
    })),
    ...compositeOperands.map((row) => ({
      id: row.metric_id,
      version: row.published_version,
      label: `复合｜${row.title}（v${row.published_version}）`,
    })),
  ];
  const updateOperand = (side: 'left' | 'right', changes: Partial<OperandState>) =>
    setForm((current) => ({ ...current, [side]: { ...current[side], ...changes } }));

  const fail = (message: string) => setError(message);

  const save = (publish = false) => {
    setError('');
    const { definition, errors } = toFormulaDefinition(form);
    if (!definition || errors.length) {
      setError(errors.join(' '));
      return;
    }
    if (!metricID) {
      perform(
        'create',
        (signal) =>
          api.createFormulaMetric(
            project.id,
            { environment, definition },
            crypto.randomUUID(),
            session.csrf_token,
            signal,
          ),
        (created) => onCreated(created.metric_id),
        (caught) => fail(caught.message),
      );
      return;
    }
    perform(
      'save',
      (signal) =>
        api.updateFormulaMetricDraft(
          project.id,
          metricID,
          draft.data?.draft_revision ?? 1,
          { definition },
          session.csrf_token,
          signal,
        ),
      (saved) => {
        setForm(fromFormulaDefinition(saved.definition));
        if (!publish) return;
        perform(
          'validate',
          (signal) =>
            api.validateFormulaMetric(
              project.id,
              metricID,
              {
                draft_revision: saved.draft_revision,
                expected_published_version: saved.published_version,
              },
              session.csrf_token,
              signal,
            ),
          (result) => {
            setValidation(result);
            perform(
              'publish',
              (signal) =>
                api.publishFormulaMetric(
                  project.id,
                  metricID,
                  {
                    draft_revision: result.draft_revision,
                    expected_published_version: result.published_version,
                    validation_hash: result.validation_hash,
                  },
                  crypto.randomUUID(),
                  session.csrf_token,
                  signal,
                ),
              () => undefined,
              (caught) => fail(caught.message),
            );
          },
          (caught) => fail(caught.message),
        );
      },
      (caught) => fail(caught.message),
    );
  };

  const validateOnly = () => {
    setError('');
    const { definition, errors } = toFormulaDefinition(form);
    if (!definition || errors.length) {
      setError(errors.join(' '));
      return;
    }
    if (!metricID) {
      setError('请先保存草稿后再校验。');
      return;
    }
    perform(
      'validate-only',
      (signal) =>
        api.updateFormulaMetricDraft(
          project.id,
          metricID,
          draft.data?.draft_revision ?? 1,
          { definition },
          session.csrf_token,
          signal,
        ),
      (saved) =>
        perform(
          'validate',
          (signal) =>
            api.validateFormulaMetric(
              project.id,
              metricID,
              {
                draft_revision: saved.draft_revision,
                expected_published_version: saved.published_version,
              },
              session.csrf_token,
              signal,
            ),
          (result) => setValidation(result),
          (caught) => fail(caught.message),
        ),
      (caught) => fail(caught.message),
    );
  };

  if (metricID && draft.isPending) return <LoadingPanel label="正在加载草稿" />;

  return (
    <div className="formula-editor section-stack">
      <Field label="指标标题" htmlFor="formula-title">
        <Input
          id="formula-title"
          value={form.title}
          maxLength={128}
          disabled={!canWrite}
          onChange={(event) => setForm((current) => ({ ...current, title: event.target.value }))}
        />
      </Field>
      <Field label="描述" htmlFor="formula-description">
        <Input
          id="formula-description"
          value={form.description}
          maxLength={2000}
          disabled={!canWrite}
          onChange={(event) =>
            setForm((current) => ({ ...current, description: event.target.value }))
          }
        />
      </Field>

      <fieldset className="formula-fieldset">
        <legend>公式表达式（两个操作数，引用已发布指标版本或常量，复合指标可继续嵌套）</legend>
        <OperandEditor
          label="左操作数"
          operand={form.left}
          options={operandOptions}
          disabled={!canWrite}
          onChange={(changes) => updateOperand('left', changes)}
        />
        <Field label="运算符" htmlFor="formula-operator">
          <Select
            id="formula-operator"
            value={form.operator}
            disabled={!canWrite}
            options={formulaOperators.map((op) => ({ value: op, label: operatorLabels[op] }))}
            onChange={(event) =>
              setForm((current) => ({
                ...current,
                operator: event.target.value as FormulaFormState['operator'],
              }))
            }
          />
        </Field>
        <OperandEditor
          label="右操作数"
          operand={form.right}
          options={operandOptions}
          disabled={!canWrite}
          onChange={(changes) => updateOperand('right', changes)}
        />
      </fieldset>

      <Field label="结果单位" htmlFor="formula-unit">
        <Select
          id="formula-unit"
          value={form.unit}
          disabled={!canWrite}
          options={formulaUnits.map((unit) => ({ value: unit, label: formulaUnitLabels[unit] }))}
          onChange={(event) =>
            setForm((current) => ({
              ...current,
              unit: event.target.value as FormulaFormState['unit'],
            }))
          }
        />
      </Field>

      {error && (
        <div className="inline-notice">
          <Alert tone="error" title={error} />
        </div>
      )}
      {validation && (
        <div className="inline-notice">
          <Alert
            tone="success"
            title={`校验通过，依赖 ${validation.dependencies.metrics.length} 个原子指标版本。`}
          />
        </div>
      )}

      {canWrite && (
        <div className="inline-actions">
          <Button variant="primary" onClick={() => save(false)} disabled={!!busy}>
            保存草稿
          </Button>
          <Button onClick={validateOnly} disabled={!!busy}>
            校验
          </Button>
          {canPublish && (
            <Button variant="primary" onClick={() => save(true)} disabled={!!busy}>
              校验并发布
            </Button>
          )}
        </div>
      )}
      {operandOptions.length === 0 && canWrite && (
        <p className="evia-field-hint">
          当前环境还没有已发布指标，可先在「独立指标」或本工作区发布后再引用；复合指标也可作为操作数继续嵌套。
        </p>
      )}
    </div>
  );
}

function OperandEditor({
  label,
  operand,
  options,
  disabled,
  onChange,
}: {
  label: string;
  operand: OperandState;
  options: FormulaOperandOption[];
  disabled: boolean;
  onChange: (changes: Partial<OperandState>) => void;
}) {
  return (
    <div className="formula-operand">
      <strong>{label}</strong>
      <Field label="操作数类型" htmlFor={`${label}-kind`}>
        <Select
          id={`${label}-kind`}
          value={operand.kind}
          disabled={disabled}
          options={[
            { value: 'metric', label: '指标引用' },
            { value: 'constant', label: '常量' },
          ]}
          onChange={(event) => onChange({ kind: event.target.value as OperandState['kind'] })}
        />
      </Field>
      {operand.kind === 'metric' ? (
        <>
          <Field label="指标（原子或复合）" htmlFor={`${label}-metric`}>
            <Select
              id={`${label}-metric`}
              value={operand.metricID}
              disabled={disabled}
              options={[
                { value: '', label: '请选择已发布指标' },
                ...options.map((option) => ({ value: option.id, label: option.label })),
              ]}
              onChange={(event) => {
                const selected = options.find((option) => option.id === event.target.value);
                onChange({
                  metricID: event.target.value,
                  version: selected ? String(selected.version) : '1',
                });
              }}
            />
          </Field>
          <Field label="引用版本" htmlFor={`${label}-version`}>
            <Input
              id={`${label}-version`}
              value={operand.version}
              inputMode="numeric"
              disabled={disabled}
              onChange={(event) => onChange({ version: event.target.value })}
            />
          </Field>
        </>
      ) : (
        <Field label="常量数值" htmlFor={`${label}-constant`}>
          <Input
            id={`${label}-constant`}
            value={operand.constant}
            inputMode="decimal"
            disabled={disabled}
            onChange={(event) => onChange({ constant: event.target.value })}
          />
        </Field>
      )}
    </div>
  );
}

function FormulaVersionsPanel({
  project,
  session,
  metricID,
  selectedVersion,
  onSelectVersion,
}: {
  project: Project;
  session: Session;
  metricID: string;
  selectedVersion: number;
  onSelectVersion: (version: number) => void;
}) {
  const versions = useQuery({
    queryKey: ['formula-metrics', project.id, session.user_id, metricID, 'versions'],
    queryFn: ({ signal }) => api.listFormulaMetricVersions(project.id, metricID, {}, signal),
  });
  const columns: DataTableColumn<FormulaMetricVersion>[] = [
    {
      key: 'version',
      header: '版本',
      render: (row) => (
        <Button variant="link" onClick={() => onSelectVersion(row.version)}>
          v{row.version}
        </Button>
      ),
    },
    { key: 'deps', header: '原子依赖', render: (row) => row.dependencies.metrics.length },
    { key: 'created', header: '发布时间', render: (row) => formatFormulaInstant(row.created_at) },
  ];
  return (
    <DataSurface label="已发布版本">
      {versions.isPending ? (
        <LoadingPanel label="正在加载版本" />
      ) : versions.isError ? (
        <ErrorPanel
          message={versions.error instanceof ApiError ? versions.error.message : '版本读取失败'}
          retry={() => void versions.refetch()}
        />
      ) : (
        <DataTable
          caption="复合指标版本"
          columns={columns}
          rows={versions.data?.items ?? []}
          rowKey={(row) => `${row.metric_id}:${row.version}`}
          minWidth={480}
          empty={<EmptyPanel description="还没有已发布版本。" />}
        />
      )}
      <p className="evia-field-hint">当前选择运行版本 v{selectedVersion}。</p>
    </DataSurface>
  );
}

function FormulaRunPanel({
  project,
  session,
  metricID,
  version,
  canRun,
}: {
  project: Project;
  session: Session;
  metricID: string;
  version: number;
  canRun: boolean;
}) {
  const [form, setForm] = useState<FormulaRunForm>(() => defaultRunForm(project.timezone));
  const [error, setError] = useState('');
  const [job, setJob] = useState<QueryJob>();
  const [result, setResult] = useState<QueryResult>();
  const [polling, setPolling] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => () => clearTimeout(timer.current), []);
  const stopPolling = () => {
    clearTimeout(timer.current);
    setPolling(false);
  };
  const poll = (queryID: string) => {
    setPolling(true);
    const tick = async () => {
      try {
        const current = await api.getQuery(project.id, queryID);
        setJob(current);
        if (current.state === 'succeeded') {
          setResult(await api.getQueryResult(project.id, queryID));
          stopPolling();
          return;
        }
        if (current.state === 'failed' || current.state === 'canceled') {
          stopPolling();
          return;
        }
        timer.current = setTimeout(tick, 700);
      } catch (caught) {
        stopPolling();
        setError(caught instanceof ApiError ? caught.message : '查询轮询失败，请重试。');
      }
    };
    void tick();
  };

  const run = () => {
    setError('');
    setResult(undefined);
    const { execute, error: buildError } = buildFormulaRun(form);
    if (buildError || !execute) {
      setError(buildError ?? '运行参数无效。');
      return;
    }
    void (async () => {
      try {
        const queued = await api.executeFormulaMetricVersion(
          project.id,
          metricID,
          version,
          execute as never,
          crypto.randomUUID(),
          session.csrf_token,
        );
        setJob(queued);
        poll(queued.query_id);
      } catch (caught) {
        setError(caught instanceof ApiError ? caught.message : '执行入队失败。');
      }
    })();
  };

  const cancel = async () => {
    if (!job) return;
    try {
      setJob(await api.cancelQuery(project.id, job.query_id, session.csrf_token));
      stopPolling();
    } catch (caught) {
      setError(caught instanceof ApiError ? caught.message : '取消失败。');
    }
  };

  const columns: DataTableColumn<Record<string, unknown>>[] = (result?.columns ?? []).map(
    (column) => ({
      key: column.key,
      header: column.label || column.key,
      render: (row) => {
        const value = row[column.key];
        return value === null || value === undefined ? '—' : String(value);
      },
    }),
  );

  return (
    <DataSurface label="复合指标运行">
      <div className="metric-fields">
        <Field label="开始时间" htmlFor="formula-run-from">
          <Input
            id="formula-run-from"
            type="datetime-local"
            value={form.from}
            max={form.to}
            onChange={(event) => setForm((current) => ({ ...current, from: event.target.value }))}
          />
        </Field>
        <Field label="结束时间" htmlFor="formula-run-to">
          <Input
            id="formula-run-to"
            type="datetime-local"
            value={form.to}
            min={form.from}
            onChange={(event) => setForm((current) => ({ ...current, to: event.target.value }))}
          />
        </Field>
        <Field label="时区" htmlFor="formula-run-tz">
          <Input
            id="formula-run-tz"
            value={form.timezone}
            onChange={(event) =>
              setForm((current) => ({ ...current, timezone: event.target.value }))
            }
          />
        </Field>
        <Field label="分桶间隔" htmlFor="formula-run-interval">
          <Select
            id="formula-run-interval"
            value={form.interval}
            options={[
              { value: '', label: '整段聚合' },
              { value: 'minute', label: '分钟' },
              { value: 'hour', label: '小时' },
              { value: 'day', label: '天' },
            ]}
            onChange={(event) =>
              setForm((current) => ({
                ...current,
                interval: event.target.value as FormulaRunForm['interval'],
              }))
            }
          />
        </Field>
        <Field label="行数上限" htmlFor="formula-run-limit">
          <Input
            id="formula-run-limit"
            value={form.limit}
            inputMode="numeric"
            onChange={(event) => setForm((current) => ({ ...current, limit: event.target.value }))}
          />
        </Field>
      </div>
      {error && (
        <div className="inline-notice">
          <Alert tone="error" title={error} />
        </div>
      )}
      <div className="inline-actions">
        <Button variant="primary" onClick={run} disabled={!canRun || polling || version < 1}>
          运行 v{version}
        </Button>
        {job && (job.state === 'queued' || job.state === 'running') && (
          <Button variant="danger" onClick={() => void cancel()}>
            取消任务
          </Button>
        )}
        {job && <Badge tone="info">{job.state}</Badge>}
      </div>
      {result && (
        <div className="metric-result">
          <p className="evia-field-hint">
            精度 {result.metadata.accuracy}，覆盖 {result.metadata.coverage.state}
          </p>
          <DataTable
            caption="复合指标运行结果"
            columns={columns}
            rows={result.rows as Record<string, unknown>[]}
            rowKey={(row) => JSON.stringify(row)}
            minWidth={480}
            empty={<EmptyPanel description="范围内没有符合条件的事实。" />}
          />
        </div>
      )}
    </DataSurface>
  );
}
