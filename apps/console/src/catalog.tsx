import { useRef, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Project,
  type Session,
  type Environment,
  type EventDefinition,
} from '@evia/api-client';
import {
  Badge,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  Field,
  Input,
  LoadingPanel,
  Modal,
  PageHeading,
  PaginationBar,
  Textarea,
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import { useUnsavedWork } from './use-unsaved-work';
import { EventEditor } from './event-editor';
import { CatalogError, isCatalogAccessError } from './catalog-helpers';
import { useCatalogScope } from './catalog-scope';

export type CatalogPageProps = { project: Project; session: Session; environment: Environment };
export function CatalogPage({ project, session, environment }: CatalogPageProps) {
  const cache = useQueryClient();
  const toast = useToast();
  const canWrite = project.permissions.includes('project.write');
  const [cursors, setCursors] = useState<string[]>([]);
  const [creating, setCreating] = useState(false);
  const [selected, setSelected] = useState<EventDefinition>();
  const [eventKey, setEventKey] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [editorDirty, setEditorDirty] = useState(false);
  const [editorBusy, setEditorBusy] = useState(false);
  const keyInput = useRef<HTMLInputElement>(null);
  const cursor = cursors.at(-1);
  const events = useQuery({
    queryKey: ['event-definitions', project.id, environment, cursor ?? ''],
    queryFn: ({ signal }) =>
      api.listEventDefinitions(project.id, { environment, cursor, limit: 20 }, signal),
  });
  const denied = isCatalogAccessError(events.error);
  const scope = useCatalogScope(
    JSON.stringify([project.id, environment, session.user_id, session.csrf_token]),
    !denied,
    { project, session },
  );
  const create = useMutation({
    onMutate: scope.capture,
    mutationFn: () =>
      api.createEventDefinition(
        project.id,
        { event_key: eventKey, display_name: name.trim(), description },
        session.csrf_token,
      ),
    onSuccess: async (event, _variables, token) => {
      if (!scope.isCurrent(token)) return;
      setCreating(false);
      setEventKey('');
      setName('');
      setDescription('');
      setCursors([]);
      setSelected(event);
      toast.success('事件已创建，请补全草稿');
      await cache.invalidateQueries({ queryKey: ['event-definitions', project.id] });
    },
  });
  const guard = useUnsavedWork({
    dirty: (creating && !!(eventKey || name || description)) || editorDirty,
    busy: create.isPending || editorBusy,
  });
  const columns: DataTableColumn<EventDefinition>[] = [
    {
      key: 'name',
      header: '事件名称',
      render: (event) => (
        <Button
          variant="link"
          onClick={() => {
            setEditorDirty(false);
            setSelected(event);
          }}
        >
          {event.display_name}
        </Button>
      ),
    },
    { key: 'key', header: '事件标识', render: (event) => <code>{event.event_key}</code> },
    {
      key: 'published',
      header: '发布版本',
      render: (event) =>
        event.published_versions.length ? (
          event.published_versions.map((version) => `v${version}`).join('、')
        ) : (
          <Badge tone="neutral">未发布</Badge>
        ),
    },
    {
      key: 'seen',
      header: '最近收到',
      render: (event) =>
        event.last_seen_at ? new Date(event.last_seen_at).toLocaleString() : '尚无记录',
    },
  ];
  return (
    <>
      <PageHeading
        title="事件定义"
        action={
          <>
            <Button loading={events.isFetching} onClick={() => void events.refetch()}>
              刷新
            </Button>
            <Button
              variant="primary"
              disabled={!canWrite || denied}
              onClick={() => {
                create.reset();
                setCreating(true);
              }}
            >
              创建事件
            </Button>
          </>
        }
      />
      {!canWrite && <p>当前权限为只读，可查看事件定义和草稿。</p>}
      <CatalogError
        error={events.error}
        retry={() => {
          if (events.error instanceof ApiError && events.error.code === 'INVALID_CURSOR')
            setCursors([]);
          else void events.refetch();
        }}
      />
      {events.isPending ? (
        <LoadingPanel label="正在加载事件" />
      ) : (
        !denied &&
        events.data && (
          <DataSurface
            label="事件列表"
            footer={
              <PaginationBar
                page={cursors.length + 1}
                count={events.data.items.length}
                hasNext={!!events.data.next_cursor}
                busy={events.isFetching}
                onPrevious={() => setCursors((values) => values.slice(0, -1))}
                onNext={() => {
                  if (events.data.next_cursor)
                    setCursors((values) => [...values, events.data.next_cursor!]);
                }}
              />
            }
          >
            <DataTable
              caption="事件列表"
              columns={columns}
              rows={events.data.items}
              rowKey={(event) => event.event_key}
              loading={events.isFetching}
              minWidth={680}
              empty={<EmptyPanel description="尚无事件定义，创建后补全字段和触发条件。" />}
            />
          </DataSurface>
        )
      )}
      {!denied && (
        <Modal
          open={creating && !denied}
          title="创建事件"
          onRequestClose={() =>
            guard.requestLeave(() => {
              setCreating(false);
              setEventKey('');
              setName('');
              setDescription('');
            })
          }
          busy={create.isPending}
          initialFocusRef={keyInput}
          footer={
            <>
              <Button
                disabled={create.isPending}
                onClick={() =>
                  guard.requestLeave(() => {
                    setCreating(false);
                    setEventKey('');
                    setName('');
                    setDescription('');
                  })
                }
              >
                取消
              </Button>
              <Button
                form="create-event"
                type="submit"
                variant="primary"
                loading={create.isPending}
                disabled={!canWrite || denied}
              >
                创建
              </Button>
            </>
          }
        >
          <CatalogError error={create.error} />
          <form
            id="create-event"
            className="console-form"
            onSubmit={(event) => {
              event.preventDefault();
              if (!create.isPending && canWrite) create.mutate();
            }}
          >
            <Field
              label="事件标识"
              htmlFor="event-key"
              required
              hint="创建后保持稳定，以小写字母开头，仅含小写字母、数字和下划线，最多 64 个字符。"
            >
              <Input
                id="event-key"
                ref={keyInput}
                value={eventKey}
                pattern="[a-z][a-z0-9_]{0,63}"
                maxLength={64}
                required
                disabled={create.isPending}
                onChange={(event) => setEventKey(event.target.value)}
              />
            </Field>
            <Field label="显示名称" htmlFor="event-name" required>
              <Input
                id="event-name"
                value={name}
                maxLength={128}
                required
                disabled={create.isPending}
                onChange={(event) => {
                  setName(event.target.value);
                  event.currentTarget.setCustomValidity(
                    event.target.value.trim() ? '' : '请输入显示名称',
                  );
                }}
              />
            </Field>
            <Field label="描述" htmlFor="event-description">
              <Textarea
                id="event-description"
                value={description}
                maxLength={2000}
                rows={3}
                disabled={create.isPending}
                onChange={(event) => setDescription(event.target.value)}
              />
            </Field>
          </form>
        </Modal>
      )}
      {!denied && selected && (
        <EventEditor
          key={selected.event_key}
          project={project}
          session={session}
          event={selected}
          onDirtyChange={setEditorDirty}
          onBusyChange={setEditorBusy}
          requestLeave={guard.requestLeave}
          onClose={() => {
            setSelected(undefined);
            setEditorDirty(false);
          }}
        />
      )}
      {guard.confirmation}
    </>
  );
}
