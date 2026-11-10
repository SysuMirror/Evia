import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type MetricDefinition,
  type MetricDraft,
  type MetricValidation,
  type Project,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  Field,
  Input,
  Panel,
  Select,
  Textarea,
  useToast,
} from '@evia/design-system';
import {
  allUnits,
  compatibleUnits,
  defaultUnit,
  describeChange,
  fromDefinition,
  isNumericOp,
  metricOps,
  opLabels,
  subjectLabels,
  metricSubjects,
  supportsApproximate,
  toDefinition,
  unitLabels,
  type EventVersionInput,
  type MetricFormState,
  type MetricOp,
  type MetricSubject,
} from './model';
import type { MetricsPerform } from './use-metrics-request';

interface EditorProps {
  project: Project;
  session: Session;
  environment: Environment;
  metricID?: string;
  canWrite: boolean;
  canPublish: boolean;
  perform: MetricsPerform;
  busy: string;
  onCreated: (metricID: string) => void;
}

export function MetricEditor({
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
  const cache = useQueryClient();
  const toast = useToast();
  const creating = !metricID;
  const [form, setForm] = useState<MetricFormState | null>(creating ? null : null);
  const [revision, setRevision] = useState(1);
  const [publishedVersion, setPublishedVersion] = useState(0);
  const [initialized, setInitialized] = useState(creating);
  const [errors, setErrors] = useState<string[]>([]);
  const [notice, setNotice] = useState('');
  const [validation, setValidation] = useState<MetricValidation>();
  const [publishPending, setPublishPending] = useState(false);

  const draft = useQuery({
    queryKey: ['metrics', project.id, session.user_id, environment, metricID, 'draft'],
    queryFn: ({ signal }) => api.getMetricDraft(project.id, metricID!, signal),
    enabled: !!metricID,
  });

  useEffect(() => {
    if (creating) {
      setForm((current) => current ?? fromDefinition(minimalDefinition()));
      setInitialized(true);
      return;
    }
    if (draft.data && !initialized) {
      setForm(fromDefinition(draft.data.definition));
      setRevision(draft.data.draft_revision);
      setPublishedVersion(draft.data.published_version);
      setInitialized(true);
    }
  }, [creating, draft.data, initialized]);

  if (!creating && draft.isPending) return <Panel title="指标定义">正在加载草稿…</Panel>;
  if (!creating && (draft.isError || !draft.data))
    return (
      <Alert
        tone="error"
        title="无法读取指标草稿"
        action={<Button onClick={() => void draft.refetch()}>重试</Button>}
      >
        {draft.error instanceof ApiError ? draft.error.message : '请稍后再试。'}
      </Alert>
    );
  if (!form) return null;

  const patch = (changes: Partial<MetricFormState>) => {
    setForm((current) => current && { ...current, ...changes });
    setValidation(undefined);
  };

  const reconcileOp = (next: Partial<MetricFormState>) => {
    const candidate = { ...form, ...next };
    // Switching aggregation can invalidate the chosen unit and field strategy.
    if (next.op !== undefined && next.op !== form.op) {
      const op = next.op as MetricOp;
      candidate.op = op;
      if (op === 'count') {
        candidate.fieldEnabled = false;
        candidate.fieldKey = '';
      } else if (isNumericOp(op)) {
        candidate.fieldEnabled = true;
      }
      candidate.unit = defaultUnit(candidate);
      if (!supportsApproximate(op)) candidate.accuracy = 'exact';
    }
    if (next.subject !== undefined || next.fieldEnabled !== undefined) {
      const allowed = compatibleUnits(candidate);
      if (!allowed.includes(candidate.unit)) candidate.unit = defaultUnit(candidate);
    }
    patch(candidate);
  };

  const settle = (error: Error): boolean => {
    if (
      error instanceof ApiError &&
      ['METRIC_DRAFT_REVISION_CONFLICT', 'METRIC_PUBLICATION_BASE_CONFLICT'].includes(error.code)
    ) {
      setNotice('草稿或发布基线已变化，已重新加载，请核对后重新校验。');
      setValidation(undefined);
      if (metricID) void cache.invalidateQueries({ queryKey: ['metrics', project.id] });
      return true;
    }
    setNotice(error.message);
    return false;
  };

  const save = () => {
    const { definition, errors: conversionErrors } = toDefinition(form);
    setErrors(conversionErrors);
    if (!definition) {
      setNotice('请先修正定义中的问题。');
      return;
    }
    setNotice('');
    if (creating) {
      const key = crypto.randomUUID();
      void perform(
        'metric.create',
        (signal) =>
          api.createMetric(
            project.id,
            { environment, definition },
            key,
            session.csrf_token,
            signal,
          ),
        (created: MetricDraft) => {
          toast.success('指标草稿已创建');
          onCreated(created.metric_id);
        },
        (error) => settle(error) || undefined,
      );
      return;
    }
    void perform(
      'metric.draft',
      (signal) =>
        api.updateMetricDraft(
          project.id,
          metricID!,
          revision,
          { definition },
          session.csrf_token,
          signal,
        ),
      (saved: MetricDraft) => {
        setRevision(saved.draft_revision);
        setPublishedVersion(saved.published_version);
        cache.setQueryData(
          ['metrics', project.id, session.user_id, environment, metricID, 'draft'],
          saved,
        );
        setValidation(undefined);
        toast.success('草稿已保存');
      },
      (error) => settle(error) || undefined,
    );
  };

  const validate = () => {
    if (creating) {
      setNotice('请先保存草稿，再进行校验。');
      return;
    }
    setNotice('');
    void perform(
      'metric.validate',
      (signal) =>
        api.validateMetric(
          project.id,
          metricID!,
          { draft_revision: revision, expected_published_version: publishedVersion },
          session.csrf_token,
          signal,
        ),
      (result: MetricValidation) => {
        setValidation(result);
        setPublishPending(false);
        toast.success('校验通过，可以发布');
      },
      (error) => settle(error) || undefined,
    );
  };

  const publish = () => {
    if (!validation) return;
    setPublishPending(true);
    const key = crypto.randomUUID();
    void perform(
      'metric.publish',
      (signal) =>
        api.publishMetric(
          project.id,
          metricID!,
          {
            draft_revision: validation.draft_revision,
            expected_published_version: validation.published_version,
            validation_hash: validation.validation_hash,
          },
          key,
          session.csrf_token,
          signal,
        ),
      () => {
        setPublishPending(false);
        setValidation(undefined);
        setPublishedVersion((current) => current + 1);
        cache.invalidateQueries({ queryKey: ['metrics', project.id] });
        toast.success('指标版本已发布');
      },
      (error) => settle(error) || undefined,
    );
  };

  const updateEvent = (index: number, changes: Partial<EventVersionInput>) =>
    patch({
      events: form.events.map((event, current) =>
        current === index ? { ...event, ...changes } : event,
      ),
    });
  const addEvent = () =>
    patch({ events: [...form.events, { event_key: '', schema_version: '1' }] });
  const removeEvent = (index: number) =>
    patch({ events: form.events.filter((_, current) => current !== index) });

  const units = compatibleUnits(form);
  const saving = busy === 'metric.create' || busy === 'metric.draft';
  const showAccuracy = supportsApproximate(form.op);
  const fieldRequired = form.fieldEnabled || isNumericOp(form.op);

  return (
    <Panel title="指标定义" action={<Badge>{creating ? '新建' : `草稿修订 ${revision}`}</Badge>}>
      <form
        className="console-form metric-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (!saving) save();
        }}
      >
        <Field label="指标标题" htmlFor="metric-title" required>
          <Input
            id="metric-title"
            value={form.title}
            maxLength={128}
            required
            disabled={!canWrite || !!busy}
            onChange={(event) => patch({ title: event.target.value })}
          />
        </Field>
        <Field label="描述" htmlFor="metric-description">
          <Textarea
            id="metric-description"
            value={form.description}
            maxLength={2000}
            rows={2}
            disabled={!canWrite || !!busy}
            onChange={(event) => patch({ description: event.target.value })}
          />
        </Field>

        <fieldset className="metric-fieldset">
          <legend>统计口径</legend>
          <div className="metric-fields">
            <Field label="统计主体" htmlFor="metric-subject">
              <Select
                id="metric-subject"
                value={form.subject}
                disabled={!canWrite || !!busy}
                options={metricSubjects.map((subject) => ({
                  value: subject,
                  label: subjectLabels[subject],
                }))}
                onChange={(event) => reconcileOp({ subject: event.target.value as MetricSubject })}
              />
            </Field>
            <Field label="聚合方式" htmlFor="metric-op">
              <Select
                id="metric-op"
                value={form.op}
                disabled={!canWrite || !!busy}
                options={metricOps.map((op) => ({ value: op, label: opLabels[op] }))}
                onChange={(event) => reconcileOp({ op: event.target.value as MetricOp })}
              />
            </Field>
            {showAccuracy && (
              <Field label="精度" htmlFor="metric-accuracy">
                <Select
                  id="metric-accuracy"
                  value={form.accuracy}
                  disabled={!canWrite || !!busy}
                  options={[
                    { value: 'exact', label: '精确 exact' },
                    { value: 'approximate', label: '近似 approximate' },
                  ]}
                  onChange={(event) =>
                    patch({ accuracy: event.target.value as MetricFormState['accuracy'] })
                  }
                />
              </Field>
            )}
            <Field label="单位" htmlFor="metric-unit">
              <Select
                id="metric-unit"
                value={form.unit}
                disabled={!canWrite || !!busy}
                options={allUnits
                  .filter((unit) => units.includes(unit))
                  .map((unit) => ({ value: unit, label: unitLabels[unit] }))}
                onChange={(event) => patch({ unit: event.target.value as MetricFormState['unit'] })}
              />
            </Field>
          </div>
          {(form.op === 'unique' || isNumericOp(form.op)) && (
            <div className="metric-field-option">
              <label className="evia-checkbox">
                <input
                  type="checkbox"
                  checked={fieldRequired}
                  disabled={isNumericOp(form.op) || !canWrite || !!busy}
                  onChange={(event) => reconcileOp({ fieldEnabled: event.target.checked })}
                />
                <span>
                  {isNumericOp(form.op)
                    ? '数值聚合必须指定数值属性字段'
                    : '按属性不同值去重（distinct_value）'}
                </span>
              </label>
              {fieldRequired && (
                <Field label="属性字段名（取自已固定的第一个事件）" htmlFor="metric-field-key">
                  <Input
                    id="metric-field-key"
                    value={form.fieldKey}
                    placeholder="如 duration_ms"
                    disabled={!canWrite || !!busy}
                    onChange={(event) => patch({ fieldKey: event.target.value })}
                  />
                </Field>
              )}
            </div>
          )}
        </fieldset>

        <fieldset className="metric-fieldset">
          <legend>固定事件版本（1–20 个，每个事件一个版本）</legend>
          {form.events.map((event, index) => (
            <div className="metric-event-row" key={index}>
              <Input
                aria-label={`事件 ${index + 1} 名称`}
                value={event.event_key}
                placeholder="事件名 event_key"
                disabled={!canWrite || !!busy}
                onChange={(change) => updateEvent(index, { event_key: change.target.value })}
              />
              <Input
                aria-label={`事件 ${index + 1} 版本`}
                value={event.schema_version}
                className="metric-event-version"
                disabled={!canWrite || !!busy}
                onChange={(change) => updateEvent(index, { schema_version: change.target.value })}
              />
              <Button
                variant="ghost"
                size="small"
                disabled={!canWrite || !!busy || form.events.length <= 1}
                onClick={() => removeEvent(index)}
              >
                移除
              </Button>
            </div>
          ))}
          <Button
            size="small"
            disabled={!canWrite || !!busy || form.events.length >= 20}
            onClick={addEvent}
          >
            添加事件版本
          </Button>
        </fieldset>

        <Field
          label="固定筛选（可选，条件树 JSON）"
          htmlFor="metric-filter"
          hint="运行时筛选会与该固定筛选取 AND，运行者无法移除固定约束。"
        >
          <Textarea
            id="metric-filter"
            className="metric-json"
            value={form.filterJSON}
            rows={4}
            placeholder='{"op":"eq","field":{"kind":"property","event_key":"page_opened","schema_version":1,"key":"page"},"value":"home"}'
            disabled={!canWrite || !!busy}
            onChange={(event) => patch({ filterJSON: event.target.value })}
          />
        </Field>

        {errors.length > 0 && (
          <div className="inline-notice">
            <Alert tone="error" title="定义存在问题">
              <ul className="metric-error-list">
                {errors.map((error) => (
                  <li key={error}>{error}</li>
                ))}
              </ul>
            </Alert>
          </div>
        )}
        {notice && (
          <div className="inline-notice">
            <Alert tone="error" title={notice} />
          </div>
        )}

        {canWrite && (
          <div className="inline-actions">
            <Button type="submit" variant="primary" loading={saving}>
              {creating ? '创建草稿' : '保存草稿'}
            </Button>
            <Button
              type="button"
              disabled={creating || !!busy}
              loading={busy === 'metric.validate'}
              onClick={validate}
            >
              校验差异
            </Button>
            {canPublish && (
              <Button
                type="button"
                variant="secondary"
                disabled={!validation || !!busy}
                loading={busy === 'metric.publish'}
                onClick={publish}
              >
                {publishPending ? '重新发布' : '发布版本'}
              </Button>
            )}
          </div>
        )}
      </form>

      {validation && <ValidationReview validation={validation} />}
    </Panel>
  );
}

function ValidationReview({ validation }: { validation: MetricValidation }) {
  return (
    <div className="metric-validation">
      <Alert tone="success" title="语义校验通过，未执行查询">
        草稿修订 {validation.draft_revision}，当前已发布版本 {validation.published_version}， 固定{' '}
        {validation.dependencies.event_versions.length} 个事件版本。
      </Alert>
      <Panel title="发布差异" variant="subtle">
        {validation.diff.length === 0 ? (
          <p className="evia-field-hint">与当前已发布版本没有差异（首次发布除外）。</p>
        ) : (
          <ul className="metric-diff-list">
            {validation.diff.map((change, index) => (
              <li key={`${change.path}-${index}`}>
                <Badge>{describeChange(change)}</Badge>
                <code>{JSON.stringify(change.before)}</code>
                <span aria-hidden="true">→</span>
                <code>{JSON.stringify(change.after)}</code>
              </li>
            ))}
          </ul>
        )}
      </Panel>
    </div>
  );
}

function minimalDefinition(): MetricDefinition {
  return {
    title: '',
    event_versions: [{ event_key: 'page_opened', schema_version: 1 }],
    subject: 'uid',
    aggregation: { op: 'count' },
    unit: 'event',
  };
}
