import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useInfiniteQuery, useQueries } from '@tanstack/react-query';
import {
  api,
  type Environment,
  type SavedAnalysisDefinition,
  type SavedAnalysisTemplate,
} from '@evia/api-client';
import {
  Button,
  CodeBlock,
  ErrorPanel,
  Field,
  Input,
  LoadingPanel,
  Modal,
  Select,
  Textarea,
} from '@evia/design-system';
import {
  fieldOperators,
  operators,
  publishedFields,
  systemFields,
  type QueryField,
  type PublishedField,
} from './analysis-model';
import {
  cloneSavedDefinition,
  flatSavedFilters,
  parseSavedAnalysisDefinition,
  parseSavedFilterValue,
  patchSavedQuery,
  replaceSavedKind,
  savedAnalysisIssues,
  savedDefinitionSummary,
  savedFieldID,
  savedFieldLabel,
  savedFilterText,
  savedMetricLabels,
  savedPins,
  savedVisualizationLabels,
  type SavedLeafFilter,
  type SavedMetric,
  type SavedPin,
  type SavedVisualization,
} from './saved-analysis-model';

export type SavedAnalysisEditorProps = {
  value: SavedAnalysisDefinition;
  onChange: (next: SavedAnalysisDefinition) => void;
  projectID: string;
  environment: Environment;
  actorID?: string;
  readOnly?: boolean;
  disabled?: boolean;
  onValidityChange?: (valid: boolean) => void;
  onPendingChange?: (pending: boolean) => void;
};
type Section = 'events' | 'metrics' | 'grouping' | 'filters' | 'funnel' | 'json';
const titles: Record<Section, string> = {
  events: '固定事件版本',
  metrics: '指标与主体',
  grouping: '分组与粒度',
  filters: '筛选条件',
  funnel: '有序漏斗',
  json: '高级 JSON',
};
const same = (left: unknown, right: unknown) => JSON.stringify(left) === JSON.stringify(right);
const message = (error: unknown) =>
  error instanceof Error ? error.message : '配置无效，请检查后重试。';
const defaultField: QueryField = { kind: 'system', key: 'event_key' };

export function SavedAnalysisEditor({
  value,
  onChange,
  projectID,
  environment,
  actorID,
  readOnly = false,
  disabled = false,
  onValidityChange,
  onPendingChange,
}: SavedAnalysisEditorProps) {
  const id = useId();
  const [section, setSection] = useState<Section | null>(null);
  const [candidate, setCandidate] = useState(value);
  const [opened, setOpened] = useState('');
  const [text, setText] = useState('');
  const [numbers, setNumbers] = useState<Record<string, string>>({});
  const [filterTexts, setFilterTexts] = useState<string[]>([]);
  const [localError, setLocalError] = useState('');
  const [pendingKind, setPendingKind] = useState<SavedAnalysisTemplate['kind']>();
  const [discarding, setDiscarding] = useState(false);
  const [touched, setTouched] = useState(false);
  const keepButton = useRef<HTMLButtonElement>(null);
  const jsonRef = useRef<HTMLTextAreaElement>(null);
  const sectionRef = useRef<HTMLFormElement>(null);
  const removedFocus = useRef<HTMLButtonElement | null>(null);
  const blocked = readOnly || disabled;
  const issues = useMemo(() => savedAnalysisIssues(value), [value]);
  const pending =
    !readOnly &&
    !!section &&
    (section === 'json'
      ? text !== opened
      : JSON.stringify({ candidate, numbers, filterTexts }) !== opened);
  useEffect(() => {
    onValidityChange?.(!issues.length && !pending && !localError);
  }, [issues.length, pending, localError, onValidityChange]);
  useEffect(() => {
    onPendingChange?.(pending);
  }, [pending, onPendingChange]);
  useEffect(() => {
    const source = removedFocus.current;
    removedFocus.current = null;
    if (!source) return;
    let interrupted = false;
    const interrupt = () => {
      interrupted = true;
    };
    document.addEventListener('pointerdown', interrupt, true);
    document.addEventListener('keydown', interrupt, true);
    const frame = requestAnimationFrame(() => {
      if (
        !interrupted &&
        (!source.isConnected || source.disabled) &&
        (document.activeElement === document.body || document.activeElement === source) &&
        document.hasFocus()
      ) {
        const target = [
          ...(sectionRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ??
            []),
        ].find((button) => button.textContent?.trim().startsWith('添加'));
        target?.focus();
      }
      document.removeEventListener('pointerdown', interrupt, true);
      document.removeEventListener('keydown', interrupt, true);
    });
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener('pointerdown', interrupt, true);
      document.removeEventListener('keydown', interrupt, true);
    };
  }, [candidate]);
  const catalog = useInfiniteQuery({
    queryKey: ['saved-analysis-editor-events', actorID ?? '', projectID, environment],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      api.listEventDefinitions(projectID, { environment, limit: 100, cursor: pageParam }, signal),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
    enabled: !readOnly && !!section && section !== 'json',
  });
  const events =
    catalog.data?.pages
      .flatMap((page) => page.items)
      .filter((event) => event.published_versions.length) ?? [];
  const pins = savedPins(value.query).filter((pin) => pin.event_key && pin.schema_version > 0);
  const versions = useQueries({
    queries: pins.map((pin) => ({
      queryKey: [
        'saved-analysis-editor-version',
        actorID ?? '',
        projectID,
        environment,
        pin.event_key,
        pin.schema_version,
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        api.getEventVersion(projectID, pin.event_key, pin.schema_version, signal),
      enabled: !!section && ['metrics', 'grouping', 'filters'].includes(section),
    })),
  });
  const fields: PublishedField[] = [
    ...systemFields.map((field) => ({
      id: `system:${field.key}`,
      label: field.label,
      type: field.type,
      reference: { kind: 'system' as const, key: field.key },
    })),
    ...versions.flatMap((version, index) =>
      version.data
        ? publishedFields(
            version.data.definition.schema,
            pins[index]!.event_key,
            pins[index]!.schema_version,
          )
        : [],
    ),
  ];
  const fieldByID = (key: string) => fields.find((field) => field.id === key);
  function open(next: Section) {
    const nextCandidate = cloneSavedDefinition(value);
    const nextNumbers = {
      limit: String(value.query.limit ?? 200),
      window: String(value.query.funnel?.window_seconds ?? 3600),
    };
    const nextFilters = flatSavedFilters(value.query.filter)?.rows.map(savedFilterText) ?? [];
    setCandidate(nextCandidate);
    setNumbers(nextNumbers);
    setFilterTexts(nextFilters);
    const json = JSON.stringify(value, null, 2);
    setText(json);
    setOpened(
      next === 'json'
        ? json
        : JSON.stringify({
            candidate: nextCandidate,
            numbers: nextNumbers,
            filterTexts: nextFilters,
          }),
    );
    setLocalError('');
    setSection(next);
  }
  function close() {
    if (disabled) return;
    if (pending) setDiscarding(true);
    else {
      setSection(null);
      setLocalError('');
    }
  }
  const changeQuery = (patch: Partial<SavedAnalysisTemplate>) => {
    setLocalError('');
    setCandidate((current) => patchSavedQuery(current, patch));
  };
  const changeRows = (
    rows: SavedLeafFilter[],
    join = flatSavedFilters(candidate.query.filter)?.join ?? 'and',
  ) => {
    const old = candidate.query.filter;
    changeQuery({
      filter: !rows.length
        ? undefined
        : rows.length === 1 && join === 'and' && old && !('args' in old)
          ? rows[0]
          : { op: join, args: rows },
    });
  };
  const leafRows = flatSavedFilters(candidate.query.filter);
  function apply() {
    if (blocked) return;
    try {
      let next = cloneSavedDefinition(candidate);
      if (section === 'json') next = parseSavedAnalysisDefinition(text);
      else {
        if (section === 'grouping')
          next.query.limit = integer(numbers.limit ?? '', 1, 500, '结果行数');
        if (section === 'funnel' && next.query.funnel)
          next.query.funnel.window_seconds = integer(numbers.window ?? '', 1, 604800, '转化窗口');
        if (section === 'filters' && leafRows) {
          const rows = leafRows.rows.map((filter, index): SavedLeafFilter => {
            if (!('value' in filter)) return filter;
            const input = filterTexts[index] ?? savedFilterText(filter);
            const field = fieldByID(savedFieldID(filter.field));
            if (!field) {
              if (input === savedFilterText(filter)) return filter;
              throw new Error('字段类型尚未加载，请重试字段目录后修改值。');
            }
            return { ...filter, value: parseSavedFilterValue(input, field.type, filter.op) };
          });
          next.query.filter = !rows.length
            ? undefined
            : rows.length === 1 && candidate.query.filter && !('args' in candidate.query.filter)
              ? rows[0]
              : { op: leafRows.join, args: rows };
        }
        const paths: Record<Exclude<Section, 'json'>, string[]> = {
          events: ['/query/event_versions'],
          metrics: ['/query/metrics', '/query/subject'],
          grouping: ['/query/group_by', '/query/interval', '/query/limit'],
          filters: ['/query/filter'],
          funnel: ['/query/funnel', '/query/subject', '/query/filter'],
        };
        const invalid = savedAnalysisIssues(next).find(
          (issue) =>
            paths[section as Exclude<Section, 'json'>].some((path) =>
              issue.path.startsWith(path),
            ) ||
            ((section === 'events' || section === 'funnel') && issue.message.includes('固定版本')),
        );
        if (invalid) throw new Error(`${invalid.path}，${invalid.message}`);
      }
      onChange(next);
      setTouched(true);
      setSection(null);
      setLocalError('');
    } catch (error) {
      setLocalError(message(error));
      requestAnimationFrame(() =>
        sectionRef.current
          ?.querySelector<HTMLElement>('[aria-invalid="true"], textarea, input, select')
          ?.focus(),
      );
    }
  }
  function fieldSelect(
    field: QueryField | undefined,
    onField: (field: QueryField | undefined) => void,
    label: string,
    options: PublishedField[] = fields,
    optional = false,
  ) {
    const current = field ? savedFieldID(field) : '';
    const choices = [...options];
    if (field && !choices.some((item) => item.id === current))
      choices.unshift({
        id: current,
        label: savedFieldLabel(field),
        type: 'unknown',
        reference: field,
      });
    const fieldID = `${id}-field-${label.replace(/\s+/g, '-')}`;
    return (
      <Field label={label} htmlFor={fieldID}>
        <Select
          id={fieldID}
          disabled={blocked}
          value={current}
          options={[
            { value: '', label: optional ? '身份主体' : '请选择字段', disabled: !optional },
            ...choices.map((item) => ({ value: item.id, label: item.label })),
          ]}
          onChange={(event) =>
            onField(
              event.target.value
                ? choices.find((item) => item.id === event.target.value)?.reference
                : undefined,
            )
          }
        />
      </Field>
    );
  }
  function pinControls(pin: SavedPin, onPin: (pin: SavedPin) => void, label: string) {
    const known = events.find((event) => event.event_key === pin.event_key);
    const eventOptions = events.map((event) => ({
      value: event.event_key,
      label: `${event.display_name} (${event.event_key})`,
    }));
    if (pin.event_key && !eventOptions.some((item) => item.value === pin.event_key))
      eventOptions.unshift({ value: pin.event_key, label: pin.event_key });
    const versionOptions =
      known?.published_versions.map((version) => ({
        value: String(version),
        label: `v${version}`,
      })) ?? [];
    if (
      pin.schema_version > 0 &&
      !versionOptions.some((item) => item.value === String(pin.schema_version))
    )
      versionOptions.unshift({
        value: String(pin.schema_version),
        label: `v${pin.schema_version}`,
      });
    const pinID = `${id}-pin-${label.replace(/\s+/g, '-')}`;
    return (
      <>
        <Field label={`${label}事件`} htmlFor={`${pinID}-event`}>
          <Select
            id={`${pinID}-event`}
            disabled={blocked}
            value={pin.event_key}
            options={[{ value: '', label: '请选择已发布事件', disabled: true }, ...eventOptions]}
            onChange={(event) => {
              setLocalError('');
              onPin({ event_key: event.target.value, schema_version: 0 });
            }}
          />
        </Field>
        <Field label={`${label}版本`} htmlFor={`${pinID}-version`}>
          <Select
            id={`${pinID}-version`}
            disabled={blocked || !pin.event_key}
            value={pin.schema_version > 0 ? String(pin.schema_version) : ''}
            options={[{ value: '', label: '请选择版本', disabled: true }, ...versionOptions]}
            onChange={(event) => {
              setLocalError('');
              onPin({ ...pin, schema_version: Number(event.target.value) });
            }}
          />
        </Field>
      </>
    );
  }
  const versionFailure = versions.find((result) => result.error);
  return (
    <div className="saved-analysis-editor section-stack">
      <div className="field-grid">
        <Field label="分析标题" htmlFor={`${id}-title`} required>
          <Input
            id={`${id}-title`}
            required
            maxLength={128}
            readOnly={readOnly}
            disabled={disabled}
            value={value.title}
            onChange={(event) => {
              setTouched(true);
              onChange({ ...value, title: event.target.value });
            }}
          />
        </Field>
        <Field label="描述" htmlFor={`${id}-description`}>
          <Textarea
            id={`${id}-description`}
            rows={2}
            maxLength={2000}
            readOnly={readOnly}
            disabled={disabled}
            value={value.description ?? ''}
            onChange={(event) => onChange({ ...value, description: event.target.value })}
          />
        </Field>
        <Field label="分析类型" htmlFor={`${id}-kind`}>
          <Select
            id={`${id}-kind`}
            value={value.query.kind}
            disabled={blocked}
            options={[
              { value: 'trend', label: '趋势分析' },
              { value: 'funnel', label: '有序漏斗' },
            ]}
            onChange={(event) =>
              setPendingKind(event.target.value as SavedAnalysisTemplate['kind'])
            }
          />
        </Field>
        <Field
          label="展示方式"
          htmlFor={`${id}-visualization`}
          error={
            touched
              ? issues.find((issue) => issue.path === '/visualization/kind')?.message
              : undefined
          }
        >
          <Select
            id={`${id}-visualization`}
            disabled={blocked}
            value={value.visualization.kind}
            options={Object.entries(savedVisualizationLabels).map(([kind, label]) => ({
              value: kind,
              label,
            }))}
            onChange={(event) => {
              setTouched(true);
              onChange({
                ...value,
                visualization: { kind: event.target.value as SavedVisualization },
              });
            }}
          />
        </Field>
      </div>
      <p className="saved-analysis-editor-help">{savedDefinitionSummary(value)}</p>
      <div
        className="saved-analysis-editor-summary inline-actions"
        role="group"
        aria-label="分析配置"
      >
        {value.query.kind === 'trend' ? (
          <>
            <Button disabled={disabled} onClick={() => open('events')}>
              事件版本 ({value.query.event_versions?.length ?? 0})
            </Button>
            <Button disabled={disabled} onClick={() => open('metrics')}>
              指标 ({value.query.metrics?.length || 1})
            </Button>
            <Button disabled={disabled} onClick={() => open('grouping')}>
              分组与粒度
            </Button>
          </>
        ) : (
          <Button disabled={disabled} onClick={() => open('funnel')}>
            漏斗步骤 ({value.query.funnel?.steps.length ?? 0})
          </Button>
        )}
        <Button disabled={disabled} onClick={() => open('filters')}>
          筛选
          {flatSavedFilters(value.query.filter) === null
            ? '（高级）'
            : ` (${flatSavedFilters(value.query.filter)?.rows.length ?? 0})`}
        </Button>
        <Button disabled={disabled} onClick={() => open('json')}>
          高级 JSON
        </Button>
      </div>
      {touched && issues.length > 0 && !readOnly && (
        <ul className="saved-analysis-editor-errors" aria-label="待完善配置">
          {issues.slice(0, 4).map((issue) => (
            <li key={`${issue.path}:${issue.message}`}>
              {issue.path}，{issue.message}
            </li>
          ))}
        </ul>
      )}
      <Modal
        open={!!section}
        title={section ? titles[section] : ''}
        onRequestClose={close}
        busy={disabled}
        initialFocusRef={section === 'json' ? jsonRef : undefined}
        footer={
          <>
            <Button disabled={disabled} onClick={close}>
              关闭
            </Button>
            {!readOnly && (
              <Button variant="primary" type="submit" form={`${id}-section`} disabled={disabled}>
                应用配置
              </Button>
            )}
          </>
        }
      >
        <form
          id={`${id}-section`}
          ref={sectionRef}
          aria-describedby={localError ? `${id}-section-error` : undefined}
          className="section-stack"
          onClickCapture={(event) => {
            const button = (event.target as HTMLElement).closest<HTMLButtonElement>('button');
            if (button?.textContent?.trim().startsWith('移除')) removedFocus.current = button;
          }}
          onSubmit={(event) => {
            event.preventDefault();
            event.stopPropagation();
            apply();
          }}
        >
          {localError && (
            <p id={`${id}-section-error`} role="alert" className="saved-analysis-editor-errors">
              {localError}
            </p>
          )}
          {section && section !== 'json' && !readOnly && (
            <>
              {catalog.isPending ? (
                <LoadingPanel label="正在加载已发布事件" />
              ) : catalog.error ? (
                <ErrorPanel message={catalog.error.message} retry={() => void catalog.refetch()} />
              ) : events.length === 0 ? (
                <p>当前环境尚无已发布事件，请先发布事件定义。</p>
              ) : null}
              {catalog.hasNextPage && (
                <Button
                  disabled={catalog.isFetchingNextPage}
                  onClick={() => void catalog.fetchNextPage()}
                  loading={catalog.isFetchingNextPage}
                >
                  加载更多事件
                </Button>
              )}
              {versions.some((result) => result.isPending) &&
                ['metrics', 'grouping', 'filters'].includes(section) && (
                  <LoadingPanel label="正在加载固定版本字段" />
                )}
              {versionFailure?.error && (
                <ErrorPanel
                  message={versionFailure.error.message}
                  retry={() =>
                    versions.forEach((result) => {
                      if (result.error) void result.refetch();
                    })
                  }
                />
              )}
            </>
          )}
          {section === 'json' && (
            <Field
              label="完整分析定义"
              htmlFor={`${id}-json`}
              hint="修改后应用配置，已有高级筛选和步骤条件会完整保留。"
            >
              <Textarea
                ref={jsonRef}
                id={`${id}-json`}
                className="saved-analysis-editor-json"
                rows={18}
                spellCheck={false}
                readOnly={readOnly}
                disabled={disabled}
                value={text}
                aria-invalid={!!localError}
                onChange={(event) => {
                  setText(event.target.value);
                  setLocalError('');
                }}
              />
            </Field>
          )}
          {section === 'events' && (
            <div className="saved-analysis-editor-rows section-stack">
              {(candidate.query.event_versions ?? []).map((pin, index) => (
                <div className="saved-analysis-editor-row" key={index}>
                  {pinControls(
                    pin,
                    (next) =>
                      changeQuery({
                        event_versions: candidate.query.event_versions!.map((item, itemIndex) =>
                          itemIndex === index ? next : item,
                        ),
                      }),
                    `第 ${index + 1} 个`,
                  )}
                  {!readOnly && (
                    <Button
                      disabled={disabled}
                      onClick={() =>
                        changeQuery({
                          event_versions: candidate.query.event_versions!.filter(
                            (_, itemIndex) => itemIndex !== index,
                          ),
                        })
                      }
                    >
                      移除版本 {index + 1}
                    </Button>
                  )}
                </div>
              ))}
              {!readOnly && (
                <Button
                  disabled={disabled || (candidate.query.event_versions?.length ?? 0) >= 20}
                  onClick={() =>
                    changeQuery({
                      event_versions: [
                        ...(candidate.query.event_versions ?? []),
                        { event_key: '', schema_version: 0 },
                      ],
                    })
                  }
                >
                  添加事件版本
                </Button>
              )}
              <p className="saved-analysis-editor-help">
                同一事件固定一个版本，属性引用须属于所选版本。调整版本时，先更新相关引用，或在高级
                JSON 中一起修改。
              </p>
            </div>
          )}
          {section === 'metrics' && (
            <>
              <Field label="身份主体" htmlFor={`${id}-subject`}>
                <Select
                  id={`${id}-subject`}
                  disabled={blocked}
                  value={candidate.query.subject ?? ''}
                  options={[
                    { value: '', label: '不按身份去重' },
                    { value: 'uid', label: '原始 UID' },
                    { value: 'anonymous', label: '匿名标识' },
                  ]}
                  onChange={(event) =>
                    changeQuery({
                      subject: (event.target.value ||
                        undefined) as SavedAnalysisTemplate['subject'],
                    })
                  }
                />
              </Field>
              {(candidate.query.metrics?.length
                ? candidate.query.metrics
                : [{ op: 'count' as const }]
              ).map((metric, index) => {
                const metrics = candidate.query.metrics?.length
                  ? candidate.query.metrics
                  : [{ op: 'count' as const }];
                const update = (next: SavedMetric) =>
                  changeQuery({
                    metrics: metrics.map((item, itemIndex) => (itemIndex === index ? next : item)),
                  });
                return (
                  <div className="saved-analysis-editor-row" key={index}>
                    <Field label={`指标 ${index + 1} 运算`} htmlFor={`${id}-metric-${index}`}>
                      <Select
                        id={`${id}-metric-${index}`}
                        disabled={blocked}
                        value={metric.op}
                        options={Object.entries(savedMetricLabels).map(([op, label]) => ({
                          value: op,
                          label,
                        }))}
                        onChange={(event) => {
                          const op = event.target.value as SavedMetric['op'];
                          update({
                            ...metric,
                            op,
                            ...(op === 'count' ? { field: undefined } : {}),
                            accuracy: op === 'unique' || op === 'p95' ? metric.accuracy : 'exact',
                          });
                        }}
                      />
                    </Field>
                    {metric.op !== 'count' &&
                      fieldSelect(
                        metric.field,
                        (field) => update({ ...metric, field }),
                        `指标 ${index + 1} 字段`,
                        fields.filter((field) =>
                          metric.op === 'unique'
                            ? !field.type.startsWith('array:')
                            : field.type === 'number' || field.type === 'integer',
                        ),
                        metric.op === 'unique',
                      )}
                    <Field label={`指标 ${index + 1} 精度`} htmlFor={`${id}-accuracy-${index}`}>
                      <Select
                        id={`${id}-accuracy-${index}`}
                        disabled={blocked || (metric.op !== 'unique' && metric.op !== 'p95')}
                        value={metric.accuracy ?? 'exact'}
                        options={[
                          { value: 'exact', label: '精确' },
                          { value: 'approximate', label: '近似' },
                        ]}
                        onChange={(event) =>
                          update({
                            ...metric,
                            accuracy: event.target.value as 'exact' | 'approximate',
                          })
                        }
                      />
                    </Field>
                    {!readOnly && (
                      <Button
                        disabled={disabled || metrics.length <= 1}
                        onClick={() =>
                          changeQuery({
                            metrics: metrics.filter((_, itemIndex) => itemIndex !== index),
                          })
                        }
                      >
                        移除指标 {index + 1}
                      </Button>
                    )}
                  </div>
                );
              })}
              {!readOnly && (
                <Button
                  disabled={disabled || (candidate.query.metrics?.length ?? 1) >= 10}
                  onClick={() =>
                    changeQuery({
                      metrics: [
                        ...(candidate.query.metrics?.length
                          ? candidate.query.metrics
                          : [{ op: 'count' as const }]),
                        { op: 'count', accuracy: 'exact' },
                      ],
                    })
                  }
                >
                  添加指标
                </Button>
              )}
            </>
          )}
          {section === 'grouping' && (
            <>
              <div className="field-grid">
                <Field label="时间粒度" htmlFor={`${id}-interval`}>
                  <Select
                    id={`${id}-interval`}
                    disabled={blocked}
                    value={candidate.query.interval ?? ''}
                    options={[
                      { value: '', label: '不按时间分组' },
                      { value: 'minute', label: '分钟' },
                      { value: 'hour', label: '小时' },
                      { value: 'day', label: '天' },
                    ]}
                    onChange={(event) =>
                      changeQuery({
                        interval: (event.target.value ||
                          undefined) as SavedAnalysisTemplate['interval'],
                      })
                    }
                  />
                </Field>
                <Field label="最多结果行数" htmlFor={`${id}-limit`}>
                  <Input
                    id={`${id}-limit`}
                    type="number"
                    min={1}
                    max={500}
                    step={1}
                    required
                    readOnly={readOnly}
                    disabled={disabled}
                    value={numbers.limit ?? ''}
                    onChange={(event) => {
                      setNumbers((current) => ({ ...current, limit: event.target.value }));
                      setLocalError('');
                    }}
                  />
                </Field>
              </div>
              {(candidate.query.group_by ?? []).map((field, index) => (
                <div className="saved-analysis-editor-row" key={index}>
                  {fieldSelect(
                    field,
                    (next) => {
                      if (next)
                        changeQuery({
                          group_by: candidate.query.group_by!.map((item, itemIndex) =>
                            itemIndex === index ? next : item,
                          ),
                        });
                    },
                    `分组 ${index + 1}`,
                    fields.filter((item) => !item.type.startsWith('array:')),
                  )}
                  {!readOnly && (
                    <Button
                      disabled={disabled}
                      onClick={() =>
                        changeQuery({
                          group_by: candidate.query.group_by!.filter(
                            (_, itemIndex) => itemIndex !== index,
                          ),
                        })
                      }
                    >
                      移除分组 {index + 1}
                    </Button>
                  )}
                </div>
              ))}
              {!readOnly && (
                <Button
                  disabled={disabled || (candidate.query.group_by?.length ?? 0) >= 3}
                  onClick={() =>
                    changeQuery({ group_by: [...(candidate.query.group_by ?? []), defaultField] })
                  }
                >
                  添加分组
                </Button>
              )}
            </>
          )}
          {section === 'filters' &&
            (leafRows ? (
              <>
                <Field label="条件关系" htmlFor={`${id}-join`}>
                  <Select
                    id={`${id}-join`}
                    disabled={blocked}
                    value={leafRows.join}
                    options={[
                      { value: 'and', label: '同时满足' },
                      { value: 'or', label: '满足任意一项' },
                    ]}
                    onChange={(event) =>
                      changeRows(leafRows.rows, event.target.value as 'and' | 'or')
                    }
                  />
                </Field>
                {leafRows.rows.map((filter, index) => {
                  const found = fieldByID(savedFieldID(filter.field));
                  const choices = found
                    ? fieldOperators(filter.field.key, found.type)
                    : Object.keys(operators);
                  const update = (next: SavedLeafFilter) =>
                    changeRows(
                      leafRows.rows.map((item, itemIndex) => (itemIndex === index ? next : item)),
                    );
                  return (
                    <div className="saved-analysis-editor-row" key={index}>
                      {fieldSelect(
                        filter.field,
                        (field) => {
                          if (field) {
                            update({ op: 'eq', field, value: '' });
                            setFilterTexts((current) =>
                              current.map((item, itemIndex) => (itemIndex === index ? '' : item)),
                            );
                          }
                        },
                        `筛选 ${index + 1} 字段`,
                      )}
                      <Field label={`筛选 ${index + 1} 运算`} htmlFor={`${id}-filter-op-${index}`}>
                        <Select
                          id={`${id}-filter-op-${index}`}
                          disabled={blocked}
                          value={filter.op}
                          options={[...new Set([filter.op, ...choices])].map((op) => ({
                            value: op,
                            label: operators[op] ?? op,
                          }))}
                          onChange={(event) => {
                            const op = event.target.value as SavedLeafFilter['op'];
                            update(
                              op === 'is_present' || op === 'is_missing'
                                ? { op, field: filter.field }
                                : {
                                    op,
                                    field: filter.field,
                                    value: 'value' in filter ? filter.value : '',
                                  },
                            );
                          }}
                        />
                      </Field>
                      {'value' in filter && (
                        <Field
                          label={`筛选 ${index + 1} 值`}
                          htmlFor={`${id}-filter-value-${index}`}
                          hint={
                            found?.type.startsWith('array:') || filter.op === 'in'
                              ? 'JSON 数组，例如 [0, 1] 或 [false, true]'
                              : found?.type === 'boolean'
                                ? 'true 或 false'
                                : undefined
                          }
                        >
                          <Input
                            id={`${id}-filter-value-${index}`}
                            readOnly={readOnly}
                            disabled={disabled}
                            value={filterTexts[index] ?? savedFilterText(filter)}
                            onChange={(event) => {
                              setFilterTexts((current) => {
                                const next = [...current];
                                next[index] = event.target.value;
                                return next;
                              });
                              setLocalError('');
                            }}
                          />
                        </Field>
                      )}
                      {!readOnly && (
                        <Button
                          disabled={disabled}
                          onClick={() => {
                            changeRows(leafRows.rows.filter((_, itemIndex) => itemIndex !== index));
                            setFilterTexts((current) =>
                              current.filter((_, itemIndex) => itemIndex !== index),
                            );
                          }}
                        >
                          移除筛选 {index + 1}
                        </Button>
                      )}
                    </div>
                  );
                })}
                {!readOnly && (
                  <Button
                    disabled={disabled || leafRows.rows.length >= 20}
                    onClick={() => {
                      changeRows([...leafRows.rows, { op: 'eq', field: defaultField, value: '' }]);
                      setFilterTexts((current) => [...current, '']);
                    }}
                  >
                    添加筛选
                  </Button>
                )}
              </>
            ) : (
              <>
                <p>当前包含嵌套条件，请使用高级 JSON 编辑，其他配置操作会保留这些条件。</p>
                <CodeBlock label="完整筛选条件">
                  {JSON.stringify(candidate.query.filter, null, 2)}
                </CodeBlock>
                <Button disabled={disabled} onClick={() => open('json')}>
                  查看高级 JSON
                </Button>
              </>
            ))}
          {section === 'funnel' && candidate.query.funnel && (
            <>
              <div className="field-grid">
                <Field label="漏斗主体" htmlFor={`${id}-funnel-subject`}>
                  <Select
                    id={`${id}-funnel-subject`}
                    disabled={blocked}
                    value={candidate.query.subject ?? 'uid'}
                    options={[
                      { value: 'uid', label: '原始 UID' },
                      { value: 'anonymous', label: '匿名标识' },
                    ]}
                    onChange={(event) =>
                      changeQuery({ subject: event.target.value as 'uid' | 'anonymous' })
                    }
                  />
                </Field>
                <Field label="转化窗口（秒）" htmlFor={`${id}-window`}>
                  <Input
                    id={`${id}-window`}
                    type="number"
                    min={1}
                    max={604800}
                    step={1}
                    required
                    readOnly={readOnly}
                    disabled={disabled}
                    value={numbers.window ?? ''}
                    onChange={(event) => {
                      setNumbers((current) => ({ ...current, window: event.target.value }));
                      setLocalError('');
                    }}
                  />
                </Field>
              </div>
              {candidate.query.funnel.steps.map((step, index) => (
                <div className="saved-analysis-editor-row" key={index}>
                  {pinControls(
                    step,
                    (pin) =>
                      changeQuery({
                        funnel: {
                          ...candidate.query.funnel!,
                          steps: candidate.query.funnel!.steps.map((item, itemIndex) =>
                            itemIndex === index ? { ...item, ...pin } : item,
                          ),
                        },
                      }),
                    `步骤 ${index + 1} `,
                  )}
                  {step.filter && <span className="saved-analysis-editor-help">含步骤筛选</span>}
                  {!readOnly && (
                    <Button
                      disabled={
                        disabled ||
                        candidate.query.funnel!.steps.length <= 2 ||
                        !!candidate.query.funnel!.same_entity?.length
                      }
                      onClick={() =>
                        changeQuery({
                          funnel: {
                            ...candidate.query.funnel!,
                            steps: candidate.query.funnel!.steps.filter(
                              (_, itemIndex) => itemIndex !== index,
                            ),
                          },
                        })
                      }
                    >
                      移除步骤 {index + 1}
                    </Button>
                  )}
                </div>
              ))}
              {!readOnly && (
                <Button
                  disabled={disabled || candidate.query.funnel.steps.length >= 8}
                  onClick={() =>
                    changeQuery({
                      funnel: {
                        ...candidate.query.funnel!,
                        steps: [
                          ...candidate.query.funnel!.steps,
                          { event_key: '', schema_version: 0 },
                        ],
                      },
                    })
                  }
                >
                  添加步骤
                </Button>
              )}
              {!!candidate.query.funnel.same_entity?.length && (
                <p className="saved-analysis-editor-help">
                  已保留 {candidate.query.funnel.same_entity.length}{' '}
                  条实体关联，存在实体关联时，请在高级 JSON
                  中一起调整步骤顺序与关联，避免改变对应关系。
                </p>
              )}
            </>
          )}
        </form>
      </Modal>
      <Modal
        open={!!pendingKind}
        title="替换分析类型？"
        onRequestClose={() => setPendingKind(undefined)}
        initialFocusRef={keepButton}
        footer={
          <>
            <Button ref={keepButton} onClick={() => setPendingKind(undefined)}>
              保留当前配置
            </Button>
            <Button
              variant="danger"
              disabled={disabled}
              onClick={() => {
                if (pendingKind) {
                  onChange(replaceSavedKind(value, pendingKind));
                  setTouched(true);
                }
                setPendingKind(undefined);
              }}
            >
              替换配置
            </Button>
          </>
        }
      >
        <p>替换会清除当前的事件版本、指标、分组、筛选和漏斗条件，标题与描述会保留。</p>
      </Modal>
      <Modal
        open={discarding}
        title="放弃本节修改？"
        onRequestClose={() => setDiscarding(false)}
        initialFocusRef={keepButton}
        footer={
          <>
            <Button ref={keepButton} onClick={() => setDiscarding(false)}>
              继续编辑
            </Button>
            <Button
              variant="danger"
              onClick={() => {
                setDiscarding(false);
                setSection(null);
                setLocalError('');
              }}
            >
              放弃本节修改
            </Button>
          </>
        }
      >
        <p>未应用的配置修改将丢失。</p>
      </Modal>
    </div>
  );
}
function integer(text: string, minimum: number, maximum: number, label: string): number {
  const value = Number(text);
  if (!text.trim() || !Number.isSafeInteger(value) || value < minimum || value > maximum)
    throw new Error(`${label}须为 ${minimum} 至 ${maximum} 的整数。`);
  return value;
}
