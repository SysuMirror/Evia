import { useCallback, useEffect, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Draft,
  type DraftInput,
  type EventDefinition,
  type Project,
  type Session,
  type ValidationResult,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  CodeBlock,
  Field,
  Input,
  LoadingPanel,
  Modal,
  Select,
  Textarea,
  useToast,
} from '@evia/design-system';
import {
  CatalogError,
  isCatalogAccessError,
  emptySchema,
  objectValue,
  parseObject,
  pretty,
  wireObject,
  type JsonObject,
} from './catalog-helpers';

import { useCatalogScope } from './catalog-scope';

type Props = {
  project: Project;
  session: Session;
  event: EventDefinition;
  onDirtyChange: (value: boolean) => void;
  onBusyChange: (value: boolean) => void;
  requestLeave: (action: () => void) => void;
  onClose: () => void;
};
const scalarOptions = [
  { value: 'string', label: '字符串' },
  { value: 'number', label: '数值' },
  { value: 'integer', label: '整数' },
  { value: 'boolean', label: '布尔值' },
];
const propertyOptions = [
  ...scalarOptions,
  ...scalarOptions.map((item) => ({ value: `array:${item.value}`, label: `${item.label}数组` })),
];
type FormState = {
  name: string;
  trigger: string;
  fact: string;
  sources: string[];
  schema: string;
  metadata: string;
  sample: string;
};
function formState(draft: Draft): FormState {
  const value = draft.definition;
  return {
    name: value.display_name ?? '',
    trigger: value.trigger ?? '',
    fact: value.fact_kind ?? '',
    sources: value.allowed_source_kinds ?? [],
    schema: pretty(value.schema ?? emptySchema),
    metadata: pretty(value.field_metadata),
    sample: pretty(value.example_properties),
  };
}
export function EventEditor(props: Props) {
  const [editingBusy, setEditingBusy] = useState(false);
  const [version, setVersion] = useState<number>();
  const published = useQuery({
    queryKey: ['event-version', props.project.id, props.event.event_key, version],
    queryFn: ({ signal }) =>
      api.getEventVersion(props.project.id, props.event.event_key, version!, signal),
    enabled: version !== undefined,
  });
  const updateBusy = useCallback(
    (value: boolean) => {
      setEditingBusy(value);
      props.onBusyChange(value);
    },
    [props.onBusyChange],
  );
  const draft = useQuery({
    queryKey: ['event-draft', props.project.id, props.event.event_key],
    queryFn: ({ signal }) => api.getEventDraft(props.project.id, props.event.event_key, signal),
  });
  const denied = isCatalogAccessError(draft.error);
  return (
    <Modal
      open
      title={denied ? '事件定义不可用' : `事件定义：${props.event.display_name}`}
      onRequestClose={() => props.requestLeave(props.onClose)}
      busy={draft.isFetching || editingBusy}
      footer={!draft.data || denied ? <Button onClick={props.onClose}>关闭</Button> : undefined}
    >
      <CatalogError error={draft.error} retry={() => void draft.refetch()} />
      {!denied && props.event.published_versions.length > 0 && (
        <section aria-label="不可变事件版本">
          <p>查看已发布版本</p>
          <div className="heading-actions">
            {props.event.published_versions.map((value) => (
              <Button key={value} size="small" onClick={() => setVersion(value)}>
                v{value}
              </Button>
            ))}
          </div>
        </section>
      )}
      <Modal
        open={version !== undefined && !denied}
        title={`已发布事件 v${version ?? ''}`}
        onRequestClose={() => setVersion(undefined)}
        footer={<Button onClick={() => setVersion(undefined)}>关闭</Button>}
      >
        <CatalogError error={published.error} retry={() => void published.refetch()} />
        {published.isPending ? (
          <LoadingPanel label="正在加载不可变事件版本" />
        ) : (
          !denied &&
          !isCatalogAccessError(published.error) &&
          published.data && (
            <>
              <p>
                <code>{published.data.event_key}</code>，v{published.data.schema_version}
                ，发布时草稿 r{published.data.draft_revision}
              </p>
              <p>此版本不可修改。后续修改请保存新草稿并创建新发布。</p>
              <CodeBlock label="已发布事件定义">{pretty(published.data.definition)}</CodeBlock>
            </>
          )
        )}
      </Modal>
      {draft.isPending ? (
        <LoadingPanel label="正在加载草稿" />
      ) : (
        !denied &&
        draft.data && <EventDraftForm {...props} initial={draft.data} onBusyChange={updateBusy} />
      )}
    </Modal>
  );
}
function EventDraftForm({
  initial,
  project,
  session,
  event,
  onDirtyChange,
  onBusyChange,
  requestLeave,
  onClose,
}: Props & { initial: Draft }) {
  const cache = useQueryClient();
  const toast = useToast();
  const [base, setBase] = useState(initial);
  const [form, setForm] = useState(() => formState(initial));
  const [baseline, setBaseline] = useState(() => JSON.stringify(formState(initial)));
  const [localError, setLocalError] = useState<Error | null>(null);
  const [latest, setLatest] = useState<Draft>();
  const [validation, setValidation] = useState<{ revision: number; result: ValidationResult }>();
  const [fieldName, setFieldName] = useState('');
  const [fieldType, setFieldType] = useState('string');
  const [removing, setRemoving] = useState<string>();
  const nameRef = useRef<HTMLInputElement>(null);
  const dirty = JSON.stringify(form) !== baseline;
  const canWrite = project.permissions.includes('project.write');
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => {
    setForm((current) => ({ ...current, [key]: value }));
    setLocalError(null);
    setValidation(undefined);
  };
  const body = (): DraftInput => {
    const schema = parseObject(form.schema, 'Schema');
    const metadata = parseObject(form.metadata, '字段元数据');
    const sample = parseObject(form.sample, '样例属性');
    if (!form.name.trim()) throw new Error('请输入显示名称');
    return {
      ...base.definition,
      display_name: form.name.trim(),
      schema: wireObject(schema),
      ...(base.definition.field_metadata !== undefined ||
      form.metadata !== pretty(base.definition.field_metadata)
        ? { field_metadata: wireObject(metadata) }
        : {}),
      ...(base.definition.trigger !== undefined || form.trigger !== ''
        ? { trigger: form.trigger }
        : {}),
      allowed_source_kinds: form.sources as DraftInput['allowed_source_kinds'],
      ...(form.fact ? { fact_kind: form.fact as DraftInput['fact_kind'] } : {}),
      ...(base.definition.example_properties !== undefined ||
      form.sample !== pretty(base.definition.example_properties)
        ? { example_properties: wireObject(sample) }
        : {}),
    };
  };
  const scope = useCatalogScope(
    JSON.stringify([project.id, event.event_key, session.user_id, session.csrf_token]),
    true,
    { project, session },
  );
  const save = useMutation({
    onMutate: scope.capture,
    onError: (error, _variables, token) => {
      if (scope.isCurrent(token) && isCatalogAccessError(error)) scope.invalidate();
    },
    mutationFn: (definition: DraftInput) =>
      api.updateEventDraft(
        project.id,
        event.event_key,
        base.revision,
        definition,
        session.csrf_token,
      ),
    onSuccess: async (saved, _variables, token) => {
      if (!scope.isCurrent(token)) return;
      setBase(saved);
      const state = formState(saved);
      setForm(state);
      setBaseline(JSON.stringify(state));
      setValidation(undefined);
      setLatest(undefined);
      toast.success(`已保存草稿 r${saved.revision}`);
      cache.setQueryData(['event-draft', project.id, event.event_key], saved);
      await cache.invalidateQueries({ queryKey: ['event-definitions', project.id] });
    },
  });
  const validate = useMutation({
    onMutate: scope.capture,
    onError: (error, _variables, token) => {
      if (scope.isCurrent(token) && isCatalogAccessError(error)) scope.invalidate();
    },
    mutationFn: () =>
      api.validateEventDraft(
        project.id,
        event.event_key,
        {
          draft_revision: base.revision,
          sample_properties: wireObject(parseObject(form.sample, '样例属性')),
        },
        session.csrf_token,
      ),
    onSuccess: (result, _variables, token) => {
      if (scope.isCurrent(token)) setValidation({ revision: base.revision, result });
    },
  });
  const fetchLatest = useMutation({
    onMutate: scope.capture,
    onError: (error, _variables, token) => {
      if (scope.isCurrent(token) && isCatalogAccessError(error)) scope.invalidate();
    },
    mutationFn: () => api.getEventDraft(project.id, event.event_key),
    onSuccess: (result, _variables, token) => {
      if (scope.isCurrent(token)) setLatest(result);
    },
  });
  const busy = save.isPending || validate.isPending || fetchLatest.isPending;
  useEffect(() => {
    onDirtyChange(dirty);
  }, [dirty, onDirtyChange]);
  useEffect(() => {
    onBusyChange(busy);
    return () => onBusyChange(false);
  }, [busy, onBusyChange]);
  const locked = busy || !canWrite;
  let schema: JsonObject | undefined;
  try {
    schema = parseObject(form.schema, 'Schema');
  } catch {
    /* Advanced editing may temporarily contain incomplete JSON. */
  }
  const properties = objectValue(schema?.properties);
  const required = Array.isArray(schema?.required)
    ? schema.required.filter((item): item is string => typeof item === 'string')
    : [];
  const updateSchema = (next: JsonObject) => set('schema', pretty(next));
  const updateProperty = (key: string, next: JsonObject) => {
    if (schema) updateSchema({ ...schema, properties: { ...properties, [key]: next } });
  };
  const hasConflict =
    (save.error instanceof ApiError && save.error.code === 'DRAFT_REVISION_CONFLICT') ||
    (validate.error instanceof ApiError && validate.error.code === 'DRAFT_REVISION_CONFLICT');
  const deniedError = [save.error, validate.error, fetchLatest.error].find(isCatalogAccessError);
  if (deniedError) return <CatalogError error={deniedError} />;
  return (
    <>
      <p>
        <code>{event.event_key}</code>，草稿 r{base.revision}，
        {event.published_versions.length
          ? `已发布 ${event.published_versions.map((value) => `v${value}`).join('、')}`
          : '尚未发布'}
        {dirty ? '，有未保存修改' : ''}
      </p>
      {!canWrite && <Alert title="当前权限为只读" />}
      <CatalogError error={localError ?? save.error ?? validate.error ?? fetchLatest.error} />
      {hasConflict && (
        <Alert
          tone="warning"
          title="服务器草稿已更新，你的输入已保留"
          action={
            <Button loading={fetchLatest.isPending} onClick={() => fetchLatest.mutate()}>
              读取最新草稿
            </Button>
          }
        >
          先比较服务器内容，再决定如何保存。
        </Alert>
      )}
      {latest && (
        <section aria-label="服务器草稿">
          <p>服务器最新版本 r{latest.revision}</p>
          <details>
            <summary>查看服务器定义</summary>
            <CodeBlock label="服务器草稿定义">{pretty(latest.definition)}</CodeBlock>
          </details>
          <div className="heading-actions">
            <Button
              disabled={locked}
              onClick={() => {
                setBase(latest);
                save.reset();
                validate.reset();
                setLatest(undefined);
                setValidation(undefined);
                toast.success('已更新比较版本，请核对后保存你的内容');
              }}
            >
              保留我的输入，基于此版本继续
            </Button>
            <Button
              disabled={locked}
              onClick={() =>
                requestLeave(() => {
                  const state = formState(latest);
                  setBase(latest);
                  setForm(state);
                  setBaseline(JSON.stringify(state));
                  setLatest(undefined);
                  save.reset();
                  validate.reset();
                  setValidation(undefined);
                })
              }
            >
              使用服务器内容
            </Button>
          </div>
        </section>
      )}
      <form
        id="event-draft-form"
        className="console-form"
        onSubmit={(submit) => {
          submit.preventDefault();
          if (locked) return;
          try {
            const definition = body();
            setLocalError(null);
            save.mutate(definition);
          } catch (error) {
            setLocalError(error as Error);
          }
        }}
      >
        <Field label="显示名称" htmlFor="draft-name" required>
          <Input
            id="draft-name"
            ref={nameRef}
            value={form.name}
            maxLength={128}
            disabled={locked}
            required
            onChange={(input) => {
              set('name', input.target.value);
              input.currentTarget.setCustomValidity(
                input.target.value.trim() ? '' : '请输入显示名称',
              );
            }}
          />
        </Field>
        <Field
          label="触发条件"
          htmlFor="draft-trigger"
          hint="说明业务代码在何时产生此事件，发布前必须填写。"
        >
          <Textarea
            id="draft-trigger"
            value={form.trigger}
            maxLength={2000}
            rows={3}
            disabled={locked}
            onChange={(input) => set('trigger', input.target.value)}
          />
        </Field>
        <Field label="事实类型" htmlFor="draft-fact">
          <Select
            id="draft-fact"
            value={form.fact}
            disabled={locked}
            options={[
              {
                value: '',
                label: '请选择，发布前必填',
                disabled: base.definition.fact_kind !== undefined,
              },
              { value: 'user_intent', label: '用户意图' },
              { value: 'client_observed', label: '客户端观察' },
              { value: 'server_confirmed', label: '服务端确认' },
            ]}
            onChange={(input) => set('fact', input.target.value)}
          />
        </Field>
        <fieldset disabled={locked}>
          <legend>允许的来源</legend>
          {['browser', 'server'].map((source) => (
            <Checkbox
              key={source}
              checked={form.sources.includes(source)}
              onChange={(input) =>
                set(
                  'sources',
                  input.target.checked
                    ? [...form.sources, source]
                    : form.sources.filter((item) => item !== source),
                )
              }
            >
              {source === 'browser' ? '浏览器' : '服务端'}
            </Checkbox>
          ))}
          {form.fact === 'server_confirmed' && form.sources.includes('browser') && (
            <p role="alert">服务端确认事件不能允许浏览器来源。</p>
          )}
        </fieldset>
        <section aria-labelledby="event-properties-heading">
          <h3 id="event-properties-heading">事件字段</h3>
          <p>
            最多 50 个平面字段，数组最多 20 项。已有约束会保留，枚举、边界和数组项规则可在高级
            Schema 编辑。
          </p>
          {!schema && <Alert tone="warning" title="请先修复高级 Schema 的 JSON 格式" />}
          {Object.entries(properties).map(([key, value]) => {
            const property = objectValue(value);
            const type =
              property.type === 'array'
                ? `array:${String(objectValue(property.items).type)}`
                : String(property.type);
            return (
              <fieldset key={key} disabled={locked || !schema}>
                <legend>
                  <code>{key}</code>
                </legend>
                <Field label="字段类型" htmlFor={`property-type-${key}`}>
                  <Select
                    id={`property-type-${key}`}
                    value={type}
                    options={
                      propertyOptions.some((option) => option.value === type)
                        ? propertyOptions
                        : [{ value: type, label: `现有类型：${type}` }, ...propertyOptions]
                    }
                    onChange={(input) => {
                      const nextType = input.target.value;
                      updateProperty(key, {
                        ...property,
                        type: nextType.startsWith('array:') ? 'array' : nextType,
                        ...(nextType.startsWith('array:')
                          ? { items: { ...objectValue(property.items), type: nextType.slice(6) } }
                          : {}),
                      });
                    }}
                  />
                </Field>
                <Checkbox
                  checked={required.includes(key)}
                  onChange={(input) => {
                    if (schema)
                      updateSchema({
                        ...schema,
                        required: input.target.checked
                          ? [...required, key]
                          : required.filter((item) => item !== key),
                      });
                  }}
                >
                  必填
                </Checkbox>
                <Field label="字段说明" htmlFor={`property-description-${key}`}>
                  <Input
                    id={`property-description-${key}`}
                    value={typeof property.description === 'string' ? property.description : ''}
                    maxLength={2000}
                    onChange={(input) =>
                      updateProperty(key, { ...property, description: input.target.value })
                    }
                  />
                </Field>
                <Button variant="danger" size="small" onClick={() => setRemoving(key)}>
                  删除字段 {key}
                </Button>
              </fieldset>
            );
          })}
          <div className="console-form">
            <Field label="新增字段名" htmlFor="new-property-name">
              <Input
                id="new-property-name"
                value={fieldName}
                maxLength={64}
                disabled={locked || !schema || Object.keys(properties).length >= 50}
                onChange={(input) => setFieldName(input.target.value)}
              />
            </Field>
            <Field label="新增字段类型" htmlFor="new-property-type">
              <Select
                id="new-property-type"
                value={fieldType}
                options={propertyOptions}
                disabled={locked || !schema}
                onChange={(input) => setFieldType(input.target.value)}
              />
            </Field>
            <Button
              disabled={locked || !schema || Object.keys(properties).length >= 50}
              onClick={() => {
                if (!schema) return;
                if (!/^[a-z][a-z0-9_]{0,63}$/.test(fieldName)) {
                  setLocalError(
                    new Error('字段名须以小写字母开头，仅含小写字母、数字和下划线，最多 64 个字符'),
                  );
                  return;
                }
                if (Object.hasOwn(properties, fieldName)) {
                  setLocalError(new Error('该字段名已存在'));
                  return;
                }
                updateProperty(
                  fieldName,
                  fieldType.startsWith('array:')
                    ? { type: 'array', items: { type: fieldType.slice(6) }, maxItems: 20 }
                    : { type: fieldType },
                );
                setFieldName('');
              }}
            >
              添加字段
            </Button>
          </div>
        </section>
        <details>
          <summary>高级 Schema</summary>
          <Field
            label="完整 JSON Schema"
            htmlFor="draft-schema"
            hint="完整保留已有规则，修改字段类型后请核对关联约束。仅支持本地扁平标量或同类型标量数组，禁止额外属性。"
          >
            <Textarea
              id="draft-schema"
              rows={14}
              spellCheck={false}
              value={form.schema}
              disabled={locked}
              onChange={(input) => set('schema', input.target.value)}
            />
          </Field>
        </details>
        <details>
          <summary>字段元数据</summary>
          <Field
            label="字段元数据 JSON"
            htmlFor="draft-metadata"
            hint="契约允许开放 JSON 元数据，未规定敏感性或单位的固定枚举。请保留团队既有字段键及其语义，元数据变更可能影响兼容性。"
          >
            <Textarea
              id="draft-metadata"
              rows={8}
              spellCheck={false}
              value={form.metadata}
              disabled={locked}
              onChange={(input) => set('metadata', input.target.value)}
            />
          </Field>
        </details>
        <Field
          label="样例属性 JSON"
          htmlFor="draft-sample"
          hint="只填写事件业务属性。保存后可显式验证该草稿版本的样例。"
        >
          <Textarea
            id="draft-sample"
            value={form.sample}
            rows={8}
            spellCheck={false}
            disabled={locked}
            onChange={(input) => set('sample', input.target.value)}
          />
        </Field>
        {validation && (
          <Alert
            tone={validation.result.valid ? 'success' : 'error'}
            title={`草稿 r${validation.revision} 的样例${validation.result.valid ? '验证通过' : '验证失败'}`}
          >
            <p>
              兼容性：
              {
                { initial: '首次发布', compatible: '兼容', breaking: '不兼容' }[
                  validation.result.compatibility
                ]
              }
              。样例通过不代表发布完整性或管道就绪。
            </p>
            {validation.result.issues.length > 0 && (
              <ul>
                {validation.result.issues.map((issue, index) => (
                  <li key={index}>
                    <code>{issue.path || '/'}</code>：{issue.message}（{issue.code}）
                  </li>
                ))}
              </ul>
            )}
          </Alert>
        )}
        <div className="heading-actions">
          <Button disabled={busy} onClick={() => requestLeave(onClose)}>
            关闭
          </Button>
          <Button
            disabled={locked || dirty}
            loading={validate.isPending}
            onClick={() => {
              try {
                parseObject(form.sample, '样例属性');
                validate.mutate();
              } catch (error) {
                setLocalError(error as Error);
              }
            }}
          >
            验证已保存样例
          </Button>
          <Button
            variant="primary"
            type="submit"
            disabled={!canWrite || !dirty || busy}
            loading={save.isPending}
          >
            保存草稿
          </Button>
        </div>
        {dirty && <p role="status">当前修改尚未发布，保存后再验证样例。</p>}
      </form>
      <Modal
        open={removing !== undefined}
        title="删除事件字段？"
        onRequestClose={() => setRemoving(undefined)}
        footer={
          <>
            <Button onClick={() => setRemoving(undefined)}>保留字段</Button>
            <Button
              variant="danger"
              disabled={locked}
              onClick={() => {
                if (!schema || removing === undefined) return;
                const remaining = { ...properties };
                delete remaining[removing];
                updateSchema({
                  ...schema,
                  properties: remaining,
                  required: required.filter((item) => item !== removing),
                });
                setRemoving(undefined);
              }}
            >
              删除字段
            </Button>
          </>
        }
      >
        <p>
          将从草稿 Schema
          移除字段及其必填要求，字段元数据和样例保持原值，请一并检查。已发布版本保持不可变。
        </p>
      </Modal>
    </>
  );
}
