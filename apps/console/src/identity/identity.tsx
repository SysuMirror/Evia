import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, ApiError, type Environment, type Project, type Session } from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  Field,
  Input,
  PageHeading,
  PaginationBar,
  Select,
  Toolbar,
  type DataTableColumn,
} from '@evia/design-system';
import {
  defaultResolveForm,
  formatInstant,
  latestCompletedRevision,
  memberKindLabel,
  resolvableRevisions,
  resolutionSummary,
  resolveErrorMessage,
  revisionStateLabel,
  revisionStateTone,
  shortID,
  validateResolveForm,
  type MemberKind,
  type ResolveFormState,
} from './model';

type PageProps = { project: Project; session: Session; environment: Environment };

const PAGE_LIMIT = 20;

const memberKindOptions: { value: MemberKind; label: string }[] = [
  { value: 'uid', label: '用户标识 uid' },
  { value: 'anonymous_id', label: '匿名标识 anonymous_id（UUID）' },
];

export function IdentityPage({ project, environment }: PageProps) {
  const allowed = project.permissions.includes('queries.read');
  const rawAllowed = project.permissions.includes('queries.raw.read');
  const [cursors, setCursors] = useState<string[]>([]);
  const [form, setForm] = useState<ResolveFormState>(defaultResolveForm());
  const [formErrors, setFormErrors] = useState<string[]>([]);
  const [lookupError, setLookupError] = useState('');
  const [selectedRevision, setSelectedRevision] = useState<number | null>(null);
  const [resolvedFor, setResolvedFor] = useState<{
    revision: number;
    kind: MemberKind;
    value: string;
  } | null>(null);

  const listKey = ['identity-revisions', project.id, environment, cursors.at(-1) ?? ''] as const;
  const revisions = useQuery({
    queryKey: listKey,
    queryFn: ({ signal }) =>
      api.listIdentityRevisions(
        project.id,
        { environment, limit: PAGE_LIMIT, cursor: cursors.at(-1) },
        signal,
      ),
    enabled: allowed,
  });

  const resolvable = resolvableRevisions(revisions.data?.items ?? []);
  const completed = latestCompletedRevision(revisions.data?.items ?? []);
  const targetRevision =
    (selectedRevision !== null && resolvable.some((item) => item.revision === selectedRevision)
      ? selectedRevision
      : completed?.revision) ?? null;

  const resolveKey = ['identity-resolve', project.id, environment, resolvedFor] as const;
  const resolution = useQuery({
    queryKey: resolveKey,
    queryFn: ({ signal }) => {
      if (!resolvedFor) throw new Error('no revision');
      return api.resolveIdentityMember(
        project.id,
        resolvedFor.revision,
        {
          environment,
          [resolvedFor.kind === 'uid' ? 'uid' : 'anonymous_id']: resolvedFor.value,
        },
        signal,
      );
    },
    enabled: allowed && rawAllowed && !!resolvedFor,
    retry: false,
  });

  if (!allowed) {
    return (
      <>
        <PageHeading title="身份修订" />
        <Alert tone="error" title="你没有读取查询的权限，无法查看身份修订。" />
      </>
    );
  }

  const patch = (partial: Partial<ResolveFormState>) => {
    setForm((current) => ({ ...current, ...partial }));
    setFormErrors([]);
    setLookupError('');
  };

  const lookup = () => {
    const errors = validateResolveForm(form);
    if (errors.length > 0) {
      setFormErrors(errors);
      return;
    }
    if (!targetRevision) {
      setLookupError('当前环境还没有带成员快照的已完成身份修订，无法解析。');
      return;
    }
    if (!rawAllowed) {
      setLookupError('身份解析需要原始身份读取权限（queries.raw.read）。');
      return;
    }
    setFormErrors([]);
    setLookupError('');
    setResolvedFor({
      revision: targetRevision,
      kind: form.memberKind,
      value: form.memberValue.trim(),
    });
  };

  const revisionOptions = resolvable.map((item, index) => ({
    value: String(item.revision),
    label:
      index === 0 && cursors.length === 0 ? `#${item.revision}（最新完成）` : `#${item.revision}`,
  }));

  const columns: DataTableColumn<import('@evia/api-client').IdentityRevision>[] = [
    { key: 'revision', header: '修订', render: (row) => <code>#{row.revision}</code> },
    {
      key: 'state',
      header: '状态',
      render: (row) => (
        <Badge tone={revisionStateTone(row.state)}>{revisionStateLabel(row.state)}</Badge>
      ),
    },
    {
      key: 'snapshot',
      header: '成员快照',
      render: (row) =>
        row.state !== 'completed' ? (
          '—'
        ) : row.snapshot_available === false ? (
          <Badge tone="neutral">无快照</Badge>
        ) : (
          <Badge tone="success">可钉版</Badge>
        ),
    },
    {
      key: 'watermark',
      header: '折叠水位',
      render: (row) => formatInstant(row.watermark_received_at),
    },
    { key: 'edges', header: '边数', render: (row) => row.edge_count },
    { key: 'persons', header: '人数', render: (row) => row.person_count },
    { key: 'members', header: '成员数', render: (row) => row.member_count },
    { key: 'built_at', header: '构建时间', render: (row) => formatInstant(row.built_at) },
    {
      key: 'error_code',
      header: '错误码',
      render: (row) => (row.error_code ? <code>{row.error_code}</code> : '—'),
    },
  ];

  const memberColumns: DataTableColumn<{
    kind: string;
    value: string;
    firstSeen: string;
    joined: number;
  }>[] = [
    { key: 'kind', header: '类型', render: (row) => memberKindLabel(row.kind) },
    { key: 'value', header: '标识', render: (row) => <code title={row.value}>{row.value}</code> },
    { key: 'first_seen', header: '首次出现', render: (row) => row.firstSeen },
    { key: 'joined', header: '加入修订', render: (row) => <code>#{row.joined}</code> },
  ];

  const summary = resolution.data ? resolutionSummary(resolution.data) : null;

  return (
    <>
      <PageHeading title="身份修订" />
      <div className="inline-notice">
        <Alert
          tone="info"
          title="身份修订由 identity-worker 从 sys.identity_link 事实折叠生成：不可变成员快照、单调合并（古老分量存活），支持按任意保留期内的历史完成修订解析与钉版查询（默认保留最近 90 天或 1000 个完成修订，取大）。"
        />
      </div>

      <DataSurface label="解析身份">
        <Toolbar label="身份解析查询">
          <div className="identity-fields">
            {revisionOptions.length > 0 && (
              <Field label="目标修订" htmlFor="identity-target-revision">
                <Select
                  id="identity-target-revision"
                  value={targetRevision ? String(targetRevision) : ''}
                  options={revisionOptions}
                  disabled={resolution.isFetching}
                  onChange={(event) => {
                    const parsed = Number.parseInt(event.target.value, 10);
                    setSelectedRevision(Number.isFinite(parsed) ? parsed : null);
                    setLookupError('');
                  }}
                />
              </Field>
            )}
            <Field label="身份类型" htmlFor="identity-member-kind">
              <Select
                id="identity-member-kind"
                value={form.memberKind}
                options={memberKindOptions}
                disabled={resolution.isFetching}
                onChange={(event) =>
                  patch({ memberKind: event.target.value as MemberKind, memberValue: '' })
                }
              />
            </Field>
            <Field
              label={form.memberKind === 'uid' ? '用户标识 uid' : '匿名标识 anonymous_id'}
              htmlFor="identity-member"
            >
              <Input
                id="identity-member"
                value={form.memberValue}
                placeholder={form.memberKind === 'uid' ? '要解析的 uid' : 'UUID 格式的匿名标识'}
                maxLength={128}
                autoComplete="off"
                disabled={resolution.isFetching}
                onChange={(event) => patch({ memberValue: event.target.value })}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') lookup();
                }}
              />
            </Field>
            <Button onClick={lookup} loading={resolution.isFetching}>
              解析
            </Button>
          </div>
        </Toolbar>
        {formErrors.length > 0 && (
          <div className="inline-notice">
            <Alert tone="error" title={formErrors.join(' ')} />
          </div>
        )}
        {lookupError && (
          <div className="inline-notice">
            <Alert tone="error" title={lookupError} />
          </div>
        )}
        {resolution.isError && (
          <div className="inline-notice">
            <Alert tone="error" title="身份解析失败">
              {resolution.error instanceof ApiError
                ? resolveErrorMessage(
                    resolution.error.status,
                    resolution.error.code,
                    resolution.error.message,
                  )
                : '请稍后再试。'}
            </Alert>
          </div>
        )}
        {summary && resolvedFor && (
          <div className="identity-resolution">
            <p className="evia-field-hint">
              修订 <code>#{resolvedFor.revision}</code> 解析结果：resolved person{' '}
              <code title={summary.person}>{shortID(summary.person)}</code>，共{' '}
              {summary.members.length} 个成员。
            </p>
            <DataTable
              caption="resolved person 成员"
              columns={memberColumns}
              rows={summary.members}
              rowKey={(row) => `${row.kind}:${row.value}`}
              minWidth={520}
              empty={<EmptyPanel description="该身份没有成员。" />}
            />
          </div>
        )}
      </DataSurface>

      <DataSurface label="修订历史">
        {revisions.isError ? (
          <Alert
            tone="error"
            title="无法读取身份修订"
            action={<Button onClick={() => void revisions.refetch()}>重试</Button>}
          >
            {revisions.error instanceof ApiError ? revisions.error.message : '请稍后再试。'}
          </Alert>
        ) : (
          <>
            <DataTable
              caption="身份修订列表"
              columns={columns}
              rows={revisions.data?.items ?? []}
              rowKey={(row) => `rev-${row.revision}`}
              minWidth={680}
              loading={revisions.isFetching}
              empty={
                <EmptyPanel description="还没有身份修订；identity-worker 折叠 sys.identity_link 后会出现在这里。" />
              }
            />
            <PaginationBar
              page={cursors.length + 1}
              count={revisions.data?.items.length ?? 0}
              hasNext={!!revisions.data?.next_cursor}
              busy={revisions.isFetching}
              onPrevious={() => setCursors((current) => current.slice(0, -1))}
              onNext={() => {
                if (revisions.data?.next_cursor)
                  setCursors((current) => [...current, revisions.data!.next_cursor!]);
              }}
            />
          </>
        )}
      </DataSurface>
    </>
  );
}
