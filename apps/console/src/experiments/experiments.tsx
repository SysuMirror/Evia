import { useEffect, useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type AnalysisCatalog,
  type Environment,
  type Experiment,
  type ExperimentCreate,
  type ExperimentUpdate,
  type ExperimentVariant,
  type Project,
  type QueryJob,
  type QueryRequest,
  type QueryResult,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  DataTable,
  EmptyPanel,
  ErrorPanel,
  Field,
  Input,
  LoadingPanel,
  Modal,
  PageHeading,
  Panel,
  Select,
  Tabs,
  Textarea,
  Toolbar,
  useToast,
  type DataTableColumn,
  type Tone,
} from '@evia/design-system';

type VariantDraft = { key: string; name: string; percent: string };
type OverrideDraft = { subject: string; variant: string };

type ExperimentFormState = {
  experimentKey: string;
  name: string;
  description: string;
  exposureEventKey: string;
  salt: string;
  controlVariant: string;
  variants: VariantDraft[];
  overrides: OverrideDraft[];
};

type AnalysisRow = {
  variant: string;
  is_control: boolean;
  exposed_subjects: number;
  contaminated_subjects: number;
  sample_share: number;
  converted_subjects: number;
  metric_value: number;
  metric_stddev: number;
  lift_absolute: number;
  lift_relative: number;
  ci_lower: number;
  ci_upper: number;
  p_value: number;
  significant: boolean;
  srm_p_value: number;
  srm_alert: boolean;
};

const statusMeta: Record<Experiment['status'], { label: string; tone: Tone | 'neutral' }> = {
  draft: { label: '草稿', tone: 'neutral' },
  running: { label: '运行中', tone: 'success' },
  paused: { label: '已暂停', tone: 'warning' },
  completed: { label: '已结束', tone: 'info' },
};

const windowOptions = [
  { value: '3600', label: '1 小时 (3600s)' },
  { value: '21600', label: '6 小时 (21600s)' },
  { value: '86400', label: '24 小时 (86400s)' },
  { value: '259200', label: '3 天 (259200s)' },
  { value: '604800', label: '7 天 (604800s)' },
  { value: '1209600', label: '14 天 (1209600s)' },
];

function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error) return error.message;
  return '操作失败，请稍后重试。';
}

function defaultForm(): ExperimentFormState {
  return {
    experimentKey: '',
    name: '',
    description: '',
    exposureEventKey: 'experiment_exposed',
    salt: '',
    controlVariant: 'control',
    variants: [
      { key: 'control', name: '对照组', percent: '50' },
      { key: 'treatment', name: '实验组', percent: '50' },
    ],
    overrides: [],
  };
}

function formFromExperiment(item: Experiment): ExperimentFormState {
  return {
    experimentKey: item.experiment_key,
    name: item.name,
    description: item.description,
    exposureEventKey: item.exposure_event_key,
    salt: item.salt,
    controlVariant: item.control_variant,
    variants: item.variants.map((v) => ({
      key: v.key,
      name: v.name,
      percent: String(Number((v.weight_bps / 100).toFixed(2))),
    })),
    overrides: Object.entries(item.overrides ?? {}).map(([subject, variant]) => ({
      subject,
      variant,
    })),
  };
}

function formatPercent(value: number, digits = 2): string {
  return `${(value * 100).toFixed(digits)}%`;
}

function formatSignedPercent(value: number, digits = 2): string {
  const prefix = value > 0 ? '+' : '';
  return `${prefix}${(value * 100).toFixed(digits)}%`;
}

function formatNumber(value: number, digits = 3): string {
  return Number.isFinite(value)
    ? value.toLocaleString('zh-CN', { maximumFractionDigits: digits })
    : '0';
}

function toDatetimeLocal(date: Date): string {
  return date.toISOString().slice(0, 16);
}

export function ExperimentsPage({
  project,
  session,
  environment,
}: {
  project: Project;
  session: Session;
  environment: Environment;
}) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const canWrite = project.permissions.includes('project.write');
  const [activeTab, setActiveTab] = useState<'manage' | 'evaluate'>('manage');

  // --- Management state ---
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<Experiment | null>(null);
  const [form, setForm] = useState<ExperimentFormState>(defaultForm);
  const [formError, setFormError] = useState<string | null>(null);
  const [selectedSnippet, setSelectedSnippet] = useState<Experiment | null>(null);

  const experimentsQuery = useQuery({
    queryKey: ['experiments', project.id, environment],
    queryFn: ({ signal }) => api.listExperiments(project.id, environment, signal),
  });

  const catalogQuery = useQuery<AnalysisCatalog>({
    queryKey: ['analysis-catalog', project.id, environment, 'experiments'],
    queryFn: ({ signal }) =>
      api.getAnalysisCatalog(project.id, { environment, limit: 200 }, signal),
  });

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['experiments', project.id, environment] });

  const saveMutation = useMutation({
    mutationFn: async () => {
      const variants: ExperimentVariant[] = form.variants.map((v) => ({
        key: v.key.trim(),
        name: v.name.trim(),
        weight_bps: Math.round(Number(v.percent) * 100),
      }));
      const overrides: Record<string, string> = {};
      for (const row of form.overrides) {
        const subject = row.subject.trim();
        if (subject && row.variant) overrides[subject] = row.variant;
      }
      if (editing) {
        const payload: ExperimentUpdate = {
          name: form.name.trim(),
          description: form.description,
          control_variant: form.controlVariant,
          exposure_event_key: form.exposureEventKey.trim() || 'experiment_exposed',
          variants,
          overrides,
          ...(form.salt.trim() ? { salt: form.salt.trim() } : {}),
        };
        return api.updateExperiment(project.id, editing.experiment_id, payload, session.csrf_token);
      }
      const payload: ExperimentCreate = {
        environment,
        experiment_key: form.experimentKey.trim(),
        name: form.name.trim(),
        description: form.description,
        control_variant: form.controlVariant,
        exposure_event_key: form.exposureEventKey.trim() || 'experiment_exposed',
        variants,
        overrides,
        ...(form.salt.trim() ? { salt: form.salt.trim() } : {}),
      };
      return api.createExperiment(project.id, payload, session.csrf_token);
    },
    onSuccess: () => {
      setModalOpen(false);
      setEditing(null);
      setFormError(null);
      toast.success(editing ? '实验配置已更新' : '实验已创建');
      void invalidate();
    },
    onError: (err) => setFormError(errorMessage(err)),
  });

  const statusMutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: Experiment['status'] }) =>
      api.changeExperimentStatus(project.id, id, { status }, session.csrf_token),
    onSuccess: (updated) => {
      toast.success(`实验状态已切换为「${statusMeta[updated.status].label}」`);
      void invalidate();
    },
    onError: (err) => toast.error(errorMessage(err)),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.deleteExperiment(project.id, id, session.csrf_token),
    onSuccess: () => {
      toast.success('实验已删除');
      void invalidate();
    },
    onError: (err) => toast.error(errorMessage(err)),
  });

  // --- Evaluation state ---
  const [evalFrom, setEvalFrom] = useState(() =>
    toDatetimeLocal(new Date(Date.now() - 7 * 86400_000)),
  );
  const [evalTo, setEvalTo] = useState(() => toDatetimeLocal(new Date()));
  const [evalSubject, setEvalSubject] = useState<'uid' | 'anonymous'>('uid');
  const [evalExposureKey, setEvalExposureKey] = useState('experiment_exposed');
  const [evalExposureVersion, setEvalExposureVersion] = useState('1');
  const [evalExperimentKeyFilter, setEvalExperimentKeyFilter] = useState('');
  const [evalVariantField, setEvalVariantField] = useState('variant');
  const [evalControlVariant, setEvalControlVariant] = useState('control');
  const [evalOutcomeKey, setEvalOutcomeKey] = useState('');
  const [evalOutcomeVersion, setEvalOutcomeVersion] = useState('1');
  const [evalMetricMode, setEvalMetricMode] = useState<'conversion' | 'numeric_mean'>('conversion');
  const [evalOutcomeValueField, setEvalOutcomeValueField] = useState('');
  const [evalWindowSeconds, setEvalWindowSeconds] = useState('86400');
  const [evalExpectedSplits, setEvalExpectedSplits] = useState<Record<string, number> | undefined>(
    undefined,
  );
  const [evalJob, setEvalJob] = useState<QueryJob | null>(null);
  const [evalResult, setEvalResult] = useState<QueryResult | null>(null);
  const [evalError, setEvalError] = useState<string | null>(null);
  const [evalRunning, setEvalRunning] = useState(false);

  const catalogEvents = useMemo(() => catalogQuery.data?.items ?? [], [catalogQuery.data]);
  const eventOptions = useMemo(() => {
    const seen = new Map<string, { event_key: string; schema_version: number; label: string }>();
    for (const ev of catalogEvents) {
      const prev = seen.get(ev.event_key);
      if (!prev || ev.schema_version > prev.schema_version) {
        seen.set(ev.event_key, {
          event_key: ev.event_key,
          schema_version: ev.schema_version,
          label: `${ev.event_key} (v${ev.schema_version}${ev.display_name ? ` / ${ev.display_name}` : ''})`,
        });
      }
    }
    return [...seen.values()].sort((a, b) => a.event_key.localeCompare(b.event_key));
  }, [catalogEvents]);

  const selectedExposureEvent = useMemo(
    () =>
      catalogEvents.find(
        (ev) =>
          ev.event_key === evalExposureKey &&
          String(ev.schema_version) === String(evalExposureVersion),
      ) ?? catalogEvents.find((ev) => ev.event_key === evalExposureKey),
    [catalogEvents, evalExposureKey, evalExposureVersion],
  );

  const selectedOutcomeEvent = useMemo(
    () =>
      catalogEvents.find(
        (ev) =>
          ev.event_key === evalOutcomeKey &&
          String(ev.schema_version) === String(evalOutcomeVersion),
      ) ?? catalogEvents.find((ev) => ev.event_key === evalOutcomeKey),
    [catalogEvents, evalOutcomeKey, evalOutcomeVersion],
  );

  const exposureScalarFields = useMemo(() => {
    if (!selectedExposureEvent) return [];
    return selectedExposureEvent.fields
      .filter((f) => f.field.kind === 'property' && !f.type.startsWith('array:'))
      .map((f) => ({ value: f.field.key, label: `${f.field.key} (${f.type})` }));
  }, [selectedExposureEvent]);

  const outcomeNumericFields = useMemo(() => {
    if (!selectedOutcomeEvent) return [];
    return selectedOutcomeEvent.fields
      .filter((f) => f.field.kind === 'property' && (f.type === 'number' || f.type === 'integer'))
      .map((f) => ({ value: f.field.key, label: `${f.field.key} (${f.type})` }));
  }, [selectedOutcomeEvent]);

  useEffect(() => {
    if (!evalOutcomeKey && eventOptions.length > 0) {
      const candidate =
        eventOptions.find((e) => e.event_key !== evalExposureKey) ?? eventOptions[0];
      if (candidate) {
        setEvalOutcomeKey(candidate.event_key);
        setEvalOutcomeVersion(String(candidate.schema_version));
      }
    }
  }, [eventOptions, evalExposureKey, evalOutcomeKey]);

  const openAnalyzeForExperiment = (exp: Experiment) => {
    const expEvent = eventOptions.find((e) => e.event_key === exp.exposure_event_key);
    setEvalExposureKey(exp.exposure_event_key);
    setEvalExposureVersion(String(expEvent?.schema_version ?? 1));
    setEvalExperimentKeyFilter(exp.experiment_key);
    setEvalVariantField('variant');
    setEvalControlVariant(exp.control_variant);
    const splits: Record<string, number> = {};
    for (const v of exp.variants) {
      if (v.weight_bps > 0) splits[v.key] = v.weight_bps;
    }
    setEvalExpectedSplits(Object.keys(splits).length >= 2 ? splits : undefined);
    setEvalError(null);
    setActiveTab('evaluate');
  };

  const runEvaluation = async () => {
    setEvalError(null);
    setEvalResult(null);
    if (!evalExposureKey || !evalOutcomeKey) {
      setEvalError('请先选择曝光事件和目标事件。');
      return;
    }
    if (evalMetricMode === 'numeric_mean' && !evalOutcomeValueField) {
      setEvalError('数值均值模式需要选择一个数值型目标属性字段。');
      return;
    }
    const expVer = Number(selectedExposureEvent?.schema_version ?? evalExposureVersion) || 1;
    const outVer = Number(selectedOutcomeEvent?.schema_version ?? evalOutcomeVersion) || 1;

    const hasExpKeyProp = selectedExposureEvent?.fields.some(
      (f) => f.field.kind === 'property' && f.field.key === 'experiment_key',
    );
    const exposureFilter =
      evalExperimentKeyFilter.trim() && hasExpKeyProp
        ? {
            op: 'eq' as const,
            field: {
              kind: 'property' as const,
              event_key: evalExposureKey,
              schema_version: expVer,
              key: 'experiment_key',
            },
            value: evalExperimentKeyFilter.trim(),
          }
        : undefined;

    const req: QueryRequest = {
      kind: 'experiment',
      environment,
      subject: evalSubject,
      timezone: 'Asia/Shanghai',
      time_range: {
        from: new Date(`${evalFrom}:00Z`).toISOString(),
        to: new Date(`${evalTo}:00Z`).toISOString(),
      },
      experiment: {
        exposure_event: {
          event_key: evalExposureKey,
          schema_version: expVer,
          ...(exposureFilter ? { filter: exposureFilter } : {}),
        },
        variant_field: {
          kind: 'property',
          event_key: evalExposureKey,
          schema_version: expVer,
          key: evalVariantField.trim() || 'variant',
        },
        control_variant: evalControlVariant.trim() || 'control',
        outcome_event: {
          event_key: evalOutcomeKey,
          schema_version: outVer,
        },
        metric_mode: evalMetricMode,
        ...(evalMetricMode === 'numeric_mean'
          ? {
              outcome_value_field: {
                kind: 'property' as const,
                event_key: evalOutcomeKey,
                schema_version: outVer,
                key: evalOutcomeValueField,
              },
            }
          : {}),
        window_seconds: Number(evalWindowSeconds) || 86400,
        ...(evalExpectedSplits ? { expected_splits: evalExpectedSplits } : {}),
      },
    };

    setEvalRunning(true);
    try {
      let job = await api.createQuery(project.id, req, session.csrf_token);
      setEvalJob(job);
      const deadline = Date.now() + 30_000;
      while ((job.state === 'queued' || job.state === 'running') && Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 400));
        job = await api.getQuery(project.id, job.query_id);
        setEvalJob(job);
      }
      if (job.state !== 'succeeded') {
        setEvalError(job.error?.code ? `查询失败 (${job.error.code})` : `查询未完成: ${job.state}`);
        return;
      }
      const res = await api.getQueryResult(project.id, job.query_id);
      setEvalResult(res);
    } catch (err) {
      setEvalError(errorMessage(err));
    } finally {
      setEvalRunning(false);
    }
  };

  const analysisRows = useMemo<AnalysisRow[]>(() => {
    if (!evalResult?.rows) return [];
    return evalResult.rows as unknown as AnalysisRow[];
  }, [evalResult]);

  const totalWeightPercent = useMemo(
    () => form.variants.reduce((acc, v) => acc + (Number(v.percent) || 0), 0),
    [form.variants],
  );

  const experimentColumns: DataTableColumn<Experiment>[] = [
    {
      key: 'experiment_key',
      header: '实验键 / 名称',
      render: (row) => (
        <div>
          <strong>{row.name}</strong>
          <div style={{ fontSize: 12, opacity: 0.72 }}>
            <code>{row.experiment_key}</code> / v{row.version}
          </div>
        </div>
      ),
    },
    {
      key: 'status',
      header: '状态',
      render: (row) => (
        <Badge tone={statusMeta[row.status].tone}>{statusMeta[row.status].label}</Badge>
      ),
    },
    {
      key: 'variants',
      header: '分流配置',
      render: (row) => (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
          {row.variants.map((v) => (
            <Badge key={v.key} tone={v.key === row.control_variant ? 'info' : 'neutral'}>
              {v.key} {(v.weight_bps / 100).toFixed(1)}%
              {v.key === row.control_variant ? ' (对照)' : ''}
            </Badge>
          ))}
        </div>
      ),
    },
    {
      key: 'exposure',
      header: '曝光事件 / 白名单',
      render: (row) => (
        <div style={{ fontSize: 13 }}>
          <code>{row.exposure_event_key}</code>
          <div style={{ fontSize: 12, opacity: 0.72 }}>
            白名单 {Object.keys(row.overrides ?? {}).length} 人
          </div>
        </div>
      ),
    },
    {
      key: 'actions',
      header: '操作',
      render: (row) => (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
          <Button size="small" variant="primary" onClick={() => openAnalyzeForExperiment(row)}>
            分析效果
          </Button>
          <Button size="small" variant="ghost" onClick={() => setSelectedSnippet(row)}>
            SDK 用法
          </Button>
          {canWrite && row.status === 'draft' && (
            <Button
              size="small"
              variant="secondary"
              loading={statusMutation.isPending}
              onClick={() => statusMutation.mutate({ id: row.experiment_id, status: 'running' })}
            >
              启动
            </Button>
          )}
          {canWrite && row.status === 'running' && (
            <Button
              size="small"
              variant="secondary"
              loading={statusMutation.isPending}
              onClick={() => statusMutation.mutate({ id: row.experiment_id, status: 'paused' })}
            >
              暂停
            </Button>
          )}
          {canWrite && row.status === 'paused' && (
            <Button
              size="small"
              variant="secondary"
              loading={statusMutation.isPending}
              onClick={() => statusMutation.mutate({ id: row.experiment_id, status: 'running' })}
            >
              恢复
            </Button>
          )}
          {canWrite && (row.status === 'running' || row.status === 'paused') && (
            <Button
              size="small"
              variant="ghost"
              loading={statusMutation.isPending}
              onClick={() => statusMutation.mutate({ id: row.experiment_id, status: 'completed' })}
            >
              结束
            </Button>
          )}
          {canWrite && row.status !== 'completed' && (
            <Button
              size="small"
              variant="ghost"
              onClick={() => {
                setEditing(row);
                setForm(formFromExperiment(row));
                setFormError(null);
                setModalOpen(true);
              }}
            >
              编辑
            </Button>
          )}
          {canWrite && row.status !== 'running' && (
            <Button
              size="small"
              variant="danger"
              loading={deleteMutation.isPending}
              onClick={() => deleteMutation.mutate(row.experiment_id)}
            >
              删除
            </Button>
          )}
        </div>
      ),
    },
  ];

  const analysisColumns: DataTableColumn<AnalysisRow>[] = [
    {
      key: 'variant',
      header: '分组',
      render: (row) => (
        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <strong>{row.variant}</strong>
          {row.is_control && <Badge tone="info">对照组</Badge>}
        </div>
      ),
    },
    {
      key: 'exposed',
      header: '暴露主体 / 占比',
      render: (row) => (
        <div>
          <strong>{row.exposed_subjects.toLocaleString('zh-CN')}</strong>
          <span style={{ marginLeft: 6, fontSize: 12, opacity: 0.72 }}>
            ({formatPercent(row.sample_share, 1)})
          </span>
          {row.contaminated_subjects > 0 && (
            <div style={{ fontSize: 12, color: 'var(--evia-warning, #b45309)' }}>
              跨组污染 {row.contaminated_subjects} 人
            </div>
          )}
        </div>
      ),
    },
    {
      key: 'metric',
      header: evalMetricMode === 'conversion' ? '转化人数 / 转化率' : '主体均值 / 标准差',
      render: (row) =>
        evalMetricMode === 'conversion' ? (
          <div>
            <strong>{formatPercent(row.metric_value, 2)}</strong>
            <div style={{ fontSize: 12, opacity: 0.72 }}>
              {row.converted_subjects.toLocaleString('zh-CN')} /{' '}
              {row.exposed_subjects.toLocaleString('zh-CN')}
            </div>
          </div>
        ) : (
          <div>
            <strong>{formatNumber(row.metric_value, 2)}</strong>
            <div style={{ fontSize: 12, opacity: 0.72 }}>
              SD {formatNumber(row.metric_stddev, 2)} (触发 {row.converted_subjects} 人)
            </div>
          </div>
        ),
    },
    {
      key: 'lift',
      header: '相对对照提升 (Lift)',
      render: (row) =>
        row.is_control ? (
          <span style={{ opacity: 0.6 }}>基线</span>
        ) : (
          <div>
            <strong>
              {evalMetricMode === 'conversion'
                ? formatSignedPercent(row.lift_absolute, 2)
                : `${row.lift_absolute > 0 ? '+' : ''}${formatNumber(row.lift_absolute, 2)}`}
            </strong>
            <div style={{ fontSize: 12, opacity: 0.72 }}>
              相对 {formatSignedPercent(row.lift_relative, 1)}
            </div>
          </div>
        ),
    },
    {
      key: 'ci',
      header: '95% 置信区间',
      render: (row) =>
        row.is_control ? (
          <span style={{ opacity: 0.6 }}>-</span>
        ) : evalMetricMode === 'conversion' ? (
          <code>
            [{formatSignedPercent(row.ci_lower, 2)}, {formatSignedPercent(row.ci_upper, 2)}]
          </code>
        ) : (
          <code>
            [{formatNumber(row.ci_lower, 2)}, {formatNumber(row.ci_upper, 2)}]
          </code>
        ),
    },
    {
      key: 'significance',
      header: '显著性 (p-value)',
      render: (row) =>
        row.is_control ? (
          <Badge tone="neutral">对照基线</Badge>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <Badge
              tone={row.significant ? (row.lift_absolute >= 0 ? 'success' : 'error') : 'neutral'}
            >
              {row.significant ? (row.lift_absolute >= 0 ? '显著正向' : '显著负向') : '未达显著'}
            </Badge>
            <span style={{ fontSize: 12, opacity: 0.72 }}>p = {row.p_value.toFixed(4)}</span>
          </div>
        ),
    },
  ];

  const hasSRMAlert = analysisRows.some((r) => r.srm_alert);
  const srmPValue = analysisRows[0]?.srm_p_value;
  const totalContaminated = analysisRows.reduce((sum, r) => sum + r.contaminated_subjects, 0);

  const manageContent = (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <Toolbar
        actions={
          canWrite ? (
            <Button
              variant="primary"
              onClick={() => {
                setEditing(null);
                setForm(defaultForm());
                setFormError(null);
                setModalOpen(true);
              }}
            >
              新建实验
            </Button>
          ) : undefined
        }
      >
        <span style={{ fontSize: 13, opacity: 0.78 }}>
          运行中的实验会自动进入签名公开配置 <code>/v1/config</code>，支持浏览器 SDK
          与服务端本地确定性分桶。
        </span>
      </Toolbar>
      {experimentsQuery.isPending ? (
        <LoadingPanel label="正在加载实验列表" />
      ) : experimentsQuery.isError ? (
        <ErrorPanel
          message={errorMessage(experimentsQuery.error)}
          retry={() => void experimentsQuery.refetch()}
        />
      ) : (experimentsQuery.data?.items.length ?? 0) === 0 ? (
        <EmptyPanel
          description="当前环境下还没有 A/B 实验。创建一个实验后即可通过 /v1/config 下发分流规则，或直接切换到「实验效果分析」分析已有曝光事件。"
          action={
            canWrite ? (
              <Button
                variant="primary"
                onClick={() => {
                  setEditing(null);
                  setForm(defaultForm());
                  setFormError(null);
                  setModalOpen(true);
                }}
              >
                新建第一个实验
              </Button>
            ) : undefined
          }
        />
      ) : (
        <DataTable
          caption="项目 A/B 实验列表"
          columns={experimentColumns}
          rows={experimentsQuery.data?.items ?? []}
          rowKey={(row) => row.experiment_id}
        />
      )}
    </div>
  );

  const evaluateContent = (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <Panel title="实验效果分析配置">
        <p style={{ margin: '0 0 12px', fontSize: 13, opacity: 0.75 }}>
          按主体首次曝光锁定唯一分组，结合 SRM 检验与双样本显著性检验对比各组表现。
        </p>
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))',
            gap: 12,
          }}
        >
          <Field label="开始时间 (UTC)" htmlFor="exp-eval-from">
            <Input
              id="exp-eval-from"
              type="datetime-local"
              value={evalFrom}
              onChange={(e) => setEvalFrom(e.target.value)}
            />
          </Field>
          <Field label="结束时间 (UTC)" htmlFor="exp-eval-to">
            <Input
              id="exp-eval-to"
              type="datetime-local"
              value={evalTo}
              onChange={(e) => setEvalTo(e.target.value)}
            />
          </Field>
          <Field label="统计主体" htmlFor="exp-eval-subject">
            <Select
              id="exp-eval-subject"
              value={evalSubject}
              options={[
                { value: 'uid', label: '原始 UID (uid)' },
                { value: 'anonymous', label: '匿名设备 (anonymous)' },
              ]}
              onChange={(e) => setEvalSubject(e.target.value as 'uid' | 'anonymous')}
            />
          </Field>
          <Field label="曝光后归因窗口" htmlFor="exp-eval-window">
            <Select
              id="exp-eval-window"
              value={evalWindowSeconds}
              options={windowOptions}
              onChange={(e) => setEvalWindowSeconds(e.target.value)}
            />
          </Field>
          <Field label="曝光事件" htmlFor="exp-eval-exposure">
            <Select
              id="exp-eval-exposure"
              value={evalExposureKey}
              options={
                eventOptions.length > 0
                  ? eventOptions.map((ev) => ({ value: ev.event_key, label: ev.label }))
                  : [{ value: evalExposureKey, label: evalExposureKey }]
              }
              onChange={(e) => {
                const key = e.target.value;
                setEvalExposureKey(key);
                const found = eventOptions.find((ev) => ev.event_key === key);
                if (found) setEvalExposureVersion(String(found.schema_version));
              }}
            />
          </Field>
          <Field label="筛选 experiment_key (可选)" htmlFor="exp-eval-key-filter">
            <Input
              id="exp-eval-key-filter"
              placeholder="例如 home_recommendation"
              value={evalExperimentKeyFilter}
              onChange={(e) => setEvalExperimentKeyFilter(e.target.value)}
            />
          </Field>
          <Field label="分组字段 (variant_field)" htmlFor="exp-eval-variant-field">
            {exposureScalarFields.length > 0 ? (
              <Select
                id="exp-eval-variant-field"
                value={evalVariantField}
                options={exposureScalarFields}
                onChange={(e) => setEvalVariantField(e.target.value)}
              />
            ) : (
              <Input
                id="exp-eval-variant-field"
                value={evalVariantField}
                onChange={(e) => setEvalVariantField(e.target.value)}
              />
            )}
          </Field>
          <Field label="对照组名称 (control_variant)" htmlFor="exp-eval-control">
            <Input
              id="exp-eval-control"
              placeholder="control / time"
              value={evalControlVariant}
              onChange={(e) => setEvalControlVariant(e.target.value)}
            />
          </Field>
          <Field label="目标转化事件" htmlFor="exp-eval-outcome">
            <Select
              id="exp-eval-outcome"
              value={evalOutcomeKey}
              options={
                eventOptions.length > 0
                  ? eventOptions.map((ev) => ({ value: ev.event_key, label: ev.label }))
                  : [{ value: evalOutcomeKey, label: evalOutcomeKey || '请先发布事件' }]
              }
              onChange={(e) => {
                const key = e.target.value;
                setEvalOutcomeKey(key);
                const found = eventOptions.find((ev) => ev.event_key === key);
                if (found) setEvalOutcomeVersion(String(found.schema_version));
                setEvalOutcomeValueField('');
              }}
            />
          </Field>
          <Field label="指标检验类型" htmlFor="exp-eval-mode">
            <Select
              id="exp-eval-mode"
              value={evalMetricMode}
              options={[
                { value: 'conversion', label: '转化率 (Two-proportion Z-test)' },
                { value: 'numeric_mean', label: '主体数值均值 (Welch t-test)' },
              ]}
              onChange={(e) => setEvalMetricMode(e.target.value as 'conversion' | 'numeric_mean')}
            />
          </Field>
          {evalMetricMode === 'numeric_mean' && (
            <Field label="目标数值属性" htmlFor="exp-eval-value-field">
              <Select
                id="exp-eval-value-field"
                value={evalOutcomeValueField}
                options={[{ value: '', label: '请选择数值属性' }, ...outcomeNumericFields]}
                onChange={(e) => setEvalOutcomeValueField(e.target.value)}
              />
            </Field>
          )}
        </div>
        <div style={{ marginTop: 14, display: 'flex', alignItems: 'center', gap: 12 }}>
          <Button variant="primary" loading={evalRunning} onClick={() => void runEvaluation()}>
            运行实验统计分析
          </Button>
          {evalJob && (
            <span style={{ fontSize: 12, opacity: 0.75 }}>
              任务状态: {evalJob.state} ({evalJob.query_id.slice(0, 8)})
            </span>
          )}
        </div>
      </Panel>

      {evalError && <ErrorPanel message={evalError} />}

      {evalResult && (
        <Panel title="实验统计结果">
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <div style={{ fontSize: 13, opacity: 0.78 }}>
              {srmPValue !== undefined
                ? `SRM 拟合优度检验 p = ${srmPValue.toFixed(4)} | 显著性阈值 alpha = 0.05`
                : '暂无匹配的曝光样本'}
            </div>
            {hasSRMAlert && (
              <Alert tone="warning" title="检测到样本比例不匹配 (SRM 告警)">
                当前实验组实际流量分配与预期比例存在极显著偏差（p = {srmPValue?.toFixed(6)} &lt;
                0.001），建议排查分流逻辑或埋点丢数后再采信实验结论。
              </Alert>
            )}
            {totalContaminated > 0 && (
              <Alert tone="info" title={`发现 ${totalContaminated} 名跨组暴露主体`}>
                同一主体在查询时间窗内命中了多个实验变体，系统已按 Intent-to-Treat
                原则以首次曝光组别归组。
              </Alert>
            )}
            {analysisRows.length === 0 ? (
              <EmptyPanel description="当前时间范围与过滤条件下没有查到任何曝光记录。" />
            ) : (
              <DataTable
                caption="实验分组统计对比结果"
                columns={analysisColumns}
                rows={analysisRows}
                rowKey={(row) => row.variant}
              />
            )}
          </div>
        </Panel>
      )}
    </div>
  );

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <PageHeading
        title="A/B 实验"
        summary="统一管理实验分流配置、签名下发 /v1/config，并基于曝光与下游事件执行 SRM 及显著性统计分析。"
      />
      <Tabs
        label="实验工作区"
        value={activeTab}
        onChange={(id) => setActiveTab(id as 'manage' | 'evaluate')}
        items={[
          { id: 'manage', label: '实验配置与分流', content: manageContent },
          { id: 'evaluate', label: '实验效果分析', content: evaluateContent },
        ]}
      />

      <Modal
        open={modalOpen}
        size="wide"
        title={editing ? `编辑实验: ${editing.experiment_key}` : '新建 A/B 实验'}
        busy={saveMutation.isPending}
        onRequestClose={() => setModalOpen(false)}
        footer={
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button
              variant="ghost"
              disabled={saveMutation.isPending}
              onClick={() => setModalOpen(false)}
            >
              取消
            </Button>
            <Button
              variant="primary"
              loading={saveMutation.isPending}
              onClick={() => saveMutation.mutate()}
            >
              {editing ? '保存变更' : '创建草稿'}
            </Button>
          </div>
        }
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {formError && <ErrorPanel message={formError} />}
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <Field label="实验键 (experiment_key)" htmlFor="exp-form-key" required>
              <Input
                id="exp-form-key"
                disabled={!!editing}
                placeholder="例如 home_recommendation"
                value={form.experimentKey}
                onChange={(e) => setForm({ ...form, experimentKey: e.target.value })}
              />
            </Field>
            <Field label="实验名称" htmlFor="exp-form-name" required>
              <Input
                id="exp-form-name"
                placeholder="例如 首页推荐排序实验"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </Field>
            <Field label="曝光事件键 (exposure_event_key)" htmlFor="exp-form-exposure">
              <Input
                id="exp-form-exposure"
                value={form.exposureEventKey}
                onChange={(e) => setForm({ ...form, exposureEventKey: e.target.value })}
              />
            </Field>
            <Field label="分桶盐值 (salt, 留空自动生成)" htmlFor="exp-form-salt">
              <Input
                id="exp-form-salt"
                placeholder="留空自动生成"
                value={form.salt}
                onChange={(e) => setForm({ ...form, salt: e.target.value })}
              />
            </Field>
          </div>
          <Field label="实验说明" htmlFor="exp-form-desc">
            <Textarea
              id="exp-form-desc"
              rows={2}
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
            />
          </Field>

          <Panel
            title={`实验分组与流量权重 (已分配 ${totalWeightPercent.toFixed(1)}% / 100%)`}
            variant="subtle"
          >
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              {form.variants.map((variant, index) => (
                <div
                  key={index}
                  style={{
                    display: 'grid',
                    gridTemplateColumns: '1fr 1fr 110px auto',
                    gap: 8,
                    alignItems: 'end',
                  }}
                >
                  <Field label="变体键" htmlFor={`var-key-${index}`}>
                    <Input
                      id={`var-key-${index}`}
                      value={variant.key}
                      onChange={(e) => {
                        const next = [...form.variants];
                        const oldKey = next[index]!.key;
                        next[index] = { ...variant, key: e.target.value };
                        setForm({
                          ...form,
                          variants: next,
                          controlVariant:
                            form.controlVariant === oldKey ? e.target.value : form.controlVariant,
                        });
                      }}
                    />
                  </Field>
                  <Field label="展示名称" htmlFor={`var-name-${index}`}>
                    <Input
                      id={`var-name-${index}`}
                      value={variant.name}
                      onChange={(e) => {
                        const next = [...form.variants];
                        next[index] = { ...variant, name: e.target.value };
                        setForm({ ...form, variants: next });
                      }}
                    />
                  </Field>
                  <Field label="流量 (%)" htmlFor={`var-pct-${index}`}>
                    <Input
                      id={`var-pct-${index}`}
                      type="number"
                      min={0}
                      max={100}
                      step={0.5}
                      value={variant.percent}
                      onChange={(e) => {
                        const next = [...form.variants];
                        next[index] = { ...variant, percent: e.target.value };
                        setForm({ ...form, variants: next });
                      }}
                    />
                  </Field>
                  <Button
                    size="small"
                    variant="ghost"
                    disabled={form.variants.length <= 2}
                    onClick={() => {
                      const next = form.variants.filter((_, i) => i !== index);
                      setForm({
                        ...form,
                        variants: next,
                        controlVariant: next.some((v) => v.key === form.controlVariant)
                          ? form.controlVariant
                          : (next[0]?.key ?? ''),
                      });
                    }}
                  >
                    移除
                  </Button>
                </div>
              ))}
              <div
                style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  marginTop: 4,
                }}
              >
                <Button
                  size="small"
                  variant="secondary"
                  disabled={form.variants.length >= 10}
                  onClick={() =>
                    setForm({
                      ...form,
                      variants: [
                        ...form.variants,
                        {
                          key: `variant_${form.variants.length}`,
                          name: `实验组 ${form.variants.length}`,
                          percent: '0',
                        },
                      ],
                    })
                  }
                >
                  添加变体
                </Button>
                <div style={{ width: 220 }}>
                  <Field label="对照组" htmlFor="exp-form-control">
                    <Select
                      id="exp-form-control"
                      value={form.controlVariant}
                      options={form.variants.map((v) => ({
                        value: v.key,
                        label: `${v.key} (${v.name})`,
                      }))}
                      onChange={(e) => setForm({ ...form, controlVariant: e.target.value })}
                    />
                  </Field>
                </div>
              </div>
            </div>
          </Panel>

          <Panel title="白名单强制分组 (Overrides)" variant="subtle">
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              {form.overrides.map((item, idx) => (
                <div
                  key={idx}
                  style={{
                    display: 'grid',
                    gridTemplateColumns: '1fr 1fr auto',
                    gap: 8,
                    alignItems: 'end',
                  }}
                >
                  <Field label="主体 UID / anonymous_id" htmlFor={`ov-sub-${idx}`}>
                    <Input
                      id={`ov-sub-${idx}`}
                      value={item.subject}
                      onChange={(e) => {
                        const next = [...form.overrides];
                        next[idx] = { ...item, subject: e.target.value };
                        setForm({ ...form, overrides: next });
                      }}
                    />
                  </Field>
                  <Field label="目标变体" htmlFor={`ov-var-${idx}`}>
                    <Select
                      id={`ov-var-${idx}`}
                      value={item.variant}
                      options={form.variants.map((v) => ({ value: v.key, label: v.key }))}
                      onChange={(e) => {
                        const next = [...form.overrides];
                        next[idx] = { ...item, variant: e.target.value };
                        setForm({ ...form, overrides: next });
                      }}
                    />
                  </Field>
                  <Button
                    size="small"
                    variant="ghost"
                    onClick={() =>
                      setForm({ ...form, overrides: form.overrides.filter((_, i) => i !== idx) })
                    }
                  >
                    移除
                  </Button>
                </div>
              ))}
              <div>
                <Button
                  size="small"
                  variant="secondary"
                  onClick={() =>
                    setForm({
                      ...form,
                      overrides: [
                        ...form.overrides,
                        { subject: '', variant: form.variants[0]?.key ?? '' },
                      ],
                    })
                  }
                >
                  添加白名单
                </Button>
              </div>
            </div>
          </Panel>
        </div>
      </Modal>

      <Modal
        open={!!selectedSnippet}
        title={selectedSnippet ? `SDK 接入代码: ${selectedSnippet.experiment_key}` : ''}
        onRequestClose={() => setSelectedSnippet(null)}
      >
        {selectedSnippet && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <p style={{ margin: 0, fontSize: 13 }}>
              当实验状态为「运行中」时，Evia 会通过签名接口 <code>GET /v1/config</code>{' '}
              下发该实验规则。客户端调用 <code>evaluateExperiment</code>{' '}
              完成零延迟本地确定性分桶并自动去重上报{' '}
              <code>{selectedSnippet.exposure_event_key}</code>。
            </p>
            <CodeBlock label="Browser SDK 示例">
              {`await evia.init();\nconst decision = evia.evaluateExperiment('${selectedSnippet.experiment_key}', {\n  fallback: '${selectedSnippet.control_variant}',\n});\n\nif (decision.variant === '${selectedSnippet.variants.find((v) => v.key !== selectedSnippet.control_variant)?.key ?? selectedSnippet.control_variant}') {\n  // 命中实验组逻辑\n}`}
            </CodeBlock>
          </div>
        )}
      </Modal>
    </div>
  );
}
