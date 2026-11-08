import { useEffect, useRef, useState } from 'react';
import { useBlocker, useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, type Project, type ProjectInput, type Session } from '@evia/api-client';
import {
  Badge,
  Button,
  CopyButton,
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
  useToast,
  type DataTableColumn,
} from '@evia/design-system';
import { useDraftNavigation } from './draft-navigation';

const defaultTimezone = 'Asia/Shanghai';
const zones = [...new Set(['UTC', defaultTimezone, ...Intl.supportedValuesOf('timeZone')])];
const zoneSet = new Set(zones);

export function ProjectsPage({ session }: { session: Session }) {
  const cache = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const { register } = useDraftNavigation();
  const [cursors, setCursors] = useState<string[]>([]);
  const [creating, setCreating] = useState(false);
  const [confirmingDiscard, setConfirmingDiscard] = useState(false);
  const [selected, setSelected] = useState<Project>();
  const [name, setName] = useState('');
  const [timezone, setTimezone] = useState(defaultTimezone);
  const [errors, setErrors] = useState<{ name?: string; timezone?: string }>({});
  const nameInput = useRef<HTMLInputElement>(null);
  const timezoneInput = useRef<HTMLInputElement>(null);
  const continueButton = useRef<HTMLButtonElement>(null);
  const discardConfirmed = useRef(false);
  const pendingAction = useRef<(() => void) | null>(null);
  const dirty = name !== '' || timezone !== defaultTimezone;
  const cursor = cursors.at(-1);
  const projects = useQuery({
    queryKey: ['projects', cursor ?? ''],
    queryFn: ({ signal }) => api.projects(cursor, signal),
  });
  const clearDraft = () => {
    setName('');
    setTimezone(defaultTimezone);
    setErrors({});
    nameInput.current?.setCustomValidity('');
    timezoneInput.current?.setCustomValidity('');
  };
  const create = useMutation({
    mutationFn: (body: ProjectInput) => api.createProject(body, session.csrf_token),
    onSuccess: async (project) => {
      setCreating(false);
      clearDraft();
      setCursors([]);
      toast.success(`已创建项目「${project.name}」`);
      await cache.invalidateQueries({ queryKey: ['projects'] });
    },
  });
  const blocker = useBlocker(creating && (dirty || create.isPending));

  useEffect(() => {
    if (!creating || (!dirty && !create.isPending)) return;
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [creating, dirty, create.isPending]);

  useEffect(
    () =>
      register((proceed) => {
        if (create.isPending) return;
        if (creating && dirty) {
          pendingAction.current = proceed;
          setConfirmingDiscard(true);
        } else proceed();
      }),
    [register, creating, dirty, create.isPending],
  );

  useEffect(() => {
    if (blocker.state !== 'blocked') return;
    if (!creating || !dirty) blocker.proceed();
    else if (!create.isPending) setConfirmingDiscard(true);
  }, [blocker, creating, dirty, create.isPending]);

  const openCreate = () => {
    create.reset();
    discardConfirmed.current = false;
    pendingAction.current = null;
    clearDraft();
    setCreating(true);
  };
  const closeCreate = () => {
    if (create.isPending || confirmingDiscard) return;
    if (dirty) setConfirmingDiscard(true);
    else setCreating(false);
  };
  const continueEditing = () => {
    pendingAction.current = null;
    if (blocker.state === 'blocked') blocker.reset();
    setConfirmingDiscard(false);
  };
  const discardDraft = () => {
    discardConfirmed.current = true;
    clearDraft();
    setConfirmingDiscard(false);
    setCreating(false);
    const proceed = pendingAction.current;
    pendingAction.current = null;
    if (blocker.state === 'blocked') blocker.proceed();
    else proceed?.();
  };
  const columns: DataTableColumn<Project>[] = [
    {
      header: '项目',
      key: 'name',
      render: (project) => (
        <Button variant="link" className="project-link" onClick={() => setSelected(project)}>
          <span className="project-name">{project.name}</span>
        </Button>
      ),
    },
    { header: '统计时区', key: 'timezone', width: 200, render: (project) => project.timezone },
    {
      header: '我的权限',
      key: 'permissions',
      width: 140,
      render: (project) => (
        <Badge tone={project.permissions.includes('project.write') ? 'info' : 'neutral'}>
          {project.permissions.includes('project.write') ? '可编辑' : '只读'}
        </Badge>
      ),
    },
    {
      header: '操作',
      key: 'actions',
      width: 140,
      render: (project) => (
        <Button variant="link" onClick={() => void navigate(`/projects/${project.id}/events`)}>
          进入项目
        </Button>
      ),
    },
  ];

  return (
    <>
      <PageHeading
        title="项目管理"
        action={
          <>
            <Button onClick={() => void projects.refetch()} loading={projects.isFetching}>
              刷新
            </Button>
            <Button variant="primary" onClick={openCreate}>
              创建项目
            </Button>
          </>
        }
      />
      {projects.isError && (
        <div className="inline-notice">
          <ErrorPanel
            message={projects.error.message}
            requestID={projects.error instanceof ApiError ? projects.error.requestID : undefined}
            retry={() => {
              if (projects.error instanceof ApiError && projects.error.code === 'INVALID_CURSOR')
                setCursors([]);
              else void projects.refetch();
            }}
          />
        </div>
      )}
      {projects.isPending ? (
        <LoadingPanel label="正在加载项目" />
      ) : projects.data ? (
        <DataSurface
          label="项目列表"
          footer={
            <PaginationBar
              page={cursors.length + 1}
              count={projects.data.items.length}
              hasNext={!!projects.data.next_cursor}
              busy={projects.isFetching}
              onPrevious={() => setCursors((values) => values.slice(0, -1))}
              onNext={() => {
                if (projects.data.next_cursor)
                  setCursors((values) => [...values, projects.data.next_cursor!]);
              }}
            />
          }
        >
          <DataTable
            caption="项目列表"
            columns={columns}
            rows={projects.data.items}
            rowKey={(project) => project.id}
            minWidth={560}
            loading={projects.isFetching}
            empty={<EmptyPanel description={cursors.length ? '这一页没有更多项目' : '暂无项目'} />}
          />
        </DataSurface>
      ) : null}
      <Modal
        title="创建项目"
        open={creating}
        onRequestClose={closeCreate}
        busy={create.isPending}
        initialFocusRef={nameInput}
        footer={
          <>
            <Button onClick={closeCreate} disabled={create.isPending}>
              取消
            </Button>
            <Button
              variant="primary"
              type="submit"
              form="create-project"
              loading={create.isPending}
            >
              创建
            </Button>
          </>
        }
      >
        {create.error && (
          <div className="inline-notice">
            <ErrorPanel
              message={create.error.message}
              requestID={create.error instanceof ApiError ? create.error.requestID : undefined}
            />
          </div>
        )}
        <form
          id="create-project"
          className="console-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (create.isPending) return;
            const nextErrors = {
              name: name.trim() ? undefined : '请输入项目名称',
              timezone: zoneSet.has(timezone) ? undefined : '请选择有效的统计时区',
            };
            setErrors(nextErrors);
            if (nextErrors.name) nameInput.current?.focus();
            if (!nextErrors.name && !nextErrors.timezone)
              create.mutate({ name: name.trim(), timezone });
          }}
        >
          <Field label="项目名称" htmlFor="project-name" error={errors.name} required>
            <Input
              id="project-name"
              name="name"
              ref={nameInput}
              placeholder="例如：社区论坛"
              maxLength={100}
              required
              value={name}
              disabled={create.isPending}
              onChange={(event) => {
                setName(event.target.value);
                event.target.setCustomValidity(event.target.value.trim() ? '' : '请输入项目名称');
                setErrors((current) => ({ ...current, name: undefined }));
              }}
              onInvalid={(event) => {
                const message = event.currentTarget.validity.tooLong
                  ? '名称不能超过 100 个字符'
                  : '请输入项目名称';
                setErrors((current) => ({ ...current, name: message }));
              }}
            />
          </Field>
          <Field
            label="统计时区"
            htmlFor="project-timezone"
            error={errors.timezone}
            required
            hint="用于划分报表中的日期，原始事件时间统一存储为 UTC。"
          >
            <Input
              id="project-timezone"
              ref={timezoneInput}
              name="timezone"
              list="project-timezones"
              autoComplete="off"
              required
              value={timezone}
              disabled={create.isPending}
              onChange={(event) => {
                setTimezone(event.target.value);
                event.target.setCustomValidity(
                  zoneSet.has(event.target.value) ? '' : '请选择有效的统计时区',
                );
                setErrors((current) => ({ ...current, timezone: undefined }));
              }}
              onInvalid={() =>
                setErrors((current) => ({ ...current, timezone: '请选择有效的统计时区' }))
              }
            />
            <datalist id="project-timezones">
              {zones.map((zone) => (
                <option key={zone} value={zone} />
              ))}
            </datalist>
          </Field>
        </form>
      </Modal>
      <Modal
        title="放弃尚未保存的项目？"
        open={confirmingDiscard}
        onRequestClose={continueEditing}
        initialFocusRef={continueButton}
        afterClose={() => {
          if (!discardConfirmed.current && creating) nameInput.current?.focus();
        }}
        footer={
          <>
            <Button ref={continueButton} onClick={continueEditing}>
              继续编辑
            </Button>
            <Button variant="danger" onClick={discardDraft}>
              放弃
            </Button>
          </>
        }
      >
        <p>当前填写的名称和时区将不会保存。</p>
      </Modal>
      <Modal
        title="项目详情"
        open={!!selected}
        onRequestClose={() => setSelected(undefined)}
        footer={
          <>
            <Button onClick={() => setSelected(undefined)}>完成</Button>
            {selected && (
              <Button
                variant="primary"
                onClick={() => void navigate(`/projects/${selected.id}/events`)}
              >
                进入项目
              </Button>
            )}
          </>
        }
      >
        {selected && (
          <dl className="project-details">
            <div>
              <dt>项目名称</dt>
              <dd>{selected.name}</dd>
            </div>
            <div>
              <dt>项目 ID</dt>
              <dd>
                <code>{selected.id}</code> <CopyButton value={selected.id} />
              </dd>
            </div>
            <div>
              <dt>统计时区</dt>
              <dd>{selected.timezone}</dd>
            </div>
            <div>
              <dt>我的权限</dt>
              <dd>{selected.permissions.includes('project.write') ? '可编辑项目' : '只读'}</dd>
            </div>
          </dl>
        )}
      </Modal>
    </>
  );
}
