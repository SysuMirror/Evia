import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type Project,
  type ProjectAuditEntry,
  type Session,
} from '@evia/api-client';
import {
  Alert,
  Badge,
  Button,
  DataSurface,
  DataTable,
  EmptyPanel,
  Field,
  Input,
  PaginationBar,
  Select,
  Toolbar,
  type DataTableColumn,
} from '@evia/design-system';
import {
  actorKindLabels,
  defaultAuditRange,
  formatInstant,
  fromLocalInputs,
  shortID,
  toLocalInput,
  validateAuditRange,
  type AuditRangeInput,
} from './model';

interface AuditPanelProps {
  project: Project;
  session: Session;
}

type AuditOutcome = '' | ProjectAuditEntry['outcome'];
type AuditEnvironment = '' | Environment;

interface SubmittedFilter {
  from: string;
  to: string;
  action: string;
  actorID: string;
  environment: AuditEnvironment;
  outcome: AuditOutcome;
  resourceKeyPrefix: string;
}

export function AuditPanel({ project, session }: AuditPanelProps) {
  const initial = defaultAuditRange();
  const [from, setFrom] = useState(toLocalInput(initial.from));
  const [to, setTo] = useState(toLocalInput(initial.to));
  const [rangeTouched, setRangeTouched] = useState(false);
  const [action, setAction] = useState('');
  const [actorID, setActorID] = useState('');
  const [environment, setEnvironment] = useState<AuditEnvironment>('');
  const [outcome, setOutcome] = useState<AuditOutcome>('');
  const [resourceKeyPrefix, setResourceKeyPrefix] = useState('');
  const [filterError, setFilterError] = useState('');
  const [submitted, setSubmitted] = useState<SubmittedFilter>({
    from: initial.from,
    to: initial.to,
    action: '',
    actorID: '',
    environment: '',
    outcome: '',
    resourceKeyPrefix: '',
  });
  const [cursors, setCursors] = useState<string[]>([]);

  const submit = () => {
    // Until the operator edits the window, every search ends at the current
    // instant so just-committed operations are never outside the half-open end.
    let range: AuditRangeInput | undefined;
    if (rangeTouched) {
      range = fromLocalInputs(from, to);
    } else {
      range = defaultAuditRange();
      setFrom(toLocalInput(range.from));
      setTo(toLocalInput(range.to));
    }
    if (!range) {
      setFilterError('请提供有效的起止时间。');
      return;
    }
    const error = validateAuditRange(range);
    if (error) {
      setFilterError(error);
      return;
    }
    setFilterError('');
    setCursors([]);
    setSubmitted({
      from: range.from,
      to: range.to,
      action: action.trim(),
      actorID: actorID.trim(),
      environment,
      outcome,
      resourceKeyPrefix: resourceKeyPrefix.trim(),
    });
  };

  const audit = useQuery({
    queryKey: ['governance', project.id, session.user_id, 'audit', submitted, cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listAuditLogs(
        project.id,
        {
          from: submitted.from,
          to: submitted.to,
          action: submitted.action || undefined,
          actorID: submitted.actorID || undefined,
          environment: submitted.environment || undefined,
          outcome: submitted.outcome || undefined,
          resourceKeyPrefix: submitted.resourceKeyPrefix || undefined,
          cursor: cursors.at(-1),
          limit: 20,
        },
        signal,
      ),
  });

  const columns: DataTableColumn<ProjectAuditEntry>[] = [
    { key: 'time', header: '时间', render: (row) => formatInstant(row.occurred_at) },
    {
      key: 'actor',
      header: '操作主体',
      render: (row) => (
        <span className="governance-actor">
          {actorKindLabels[row.actor_kind] ?? row.actor_kind}
          <code>{shortID(row.actor_id)}</code>
        </span>
      ),
    },
    { key: 'environment', header: '环境', render: (row) => row.environment ?? '—' },
    { key: 'action', header: '操作', render: (row) => <code>{row.action}</code> },
    {
      key: 'outcome',
      header: '结果',
      render: (row) => (
        <Badge tone={row.outcome === 'success' ? 'success' : 'error'}>
          {row.outcome === 'success' ? '成功' : '失败'}
        </Badge>
      ),
    },
    { key: 'resource', header: '资源', render: (row) => <code>{shortID(row.resource_key)}</code> },
    {
      key: 'credential',
      header: '凭据',
      render: (row) => (row.credential_id ? <code>{shortID(row.credential_id)}</code> : '—'),
    },
  ];

  return (
    <DataSurface label="项目审计">
      <Toolbar label="审计筛选">
        <div className="governance-fields governance-filters">
          <Field label="开始时间" htmlFor="governance-audit-from">
            <Input
              id="governance-audit-from"
              type="datetime-local"
              value={from}
              max={to}
              onChange={(event) => {
                setFrom(event.target.value);
                setRangeTouched(true);
              }}
            />
          </Field>
          <Field label="结束时间" htmlFor="governance-audit-to">
            <Input
              id="governance-audit-to"
              type="datetime-local"
              value={to}
              min={from}
              onChange={(event) => {
                setTo(event.target.value);
                setRangeTouched(true);
              }}
            />
          </Field>
          <Field label="操作（精确）" htmlFor="governance-audit-action">
            <Input
              id="governance-audit-action"
              value={action}
              maxLength={128}
              placeholder="如 project.settings.update"
              autoComplete="off"
              onChange={(event) => setAction(event.target.value)}
            />
          </Field>
          <Field label="主体 UUID" htmlFor="governance-audit-actor">
            <Input
              id="governance-audit-actor"
              value={actorID}
              autoComplete="off"
              onChange={(event) => setActorID(event.target.value)}
            />
          </Field>
          <Field label="环境" htmlFor="governance-audit-environment">
            <Select
              id="governance-audit-environment"
              value={environment}
              options={[
                { value: '', label: '全部环境' },
                { value: 'development', label: 'development' },
                { value: 'staging', label: 'staging' },
                { value: 'production', label: 'production' },
              ]}
              onChange={(event) => setEnvironment(event.target.value as AuditEnvironment)}
            />
          </Field>
          <Field label="结果" htmlFor="governance-audit-outcome">
            <Select
              id="governance-audit-outcome"
              value={outcome}
              options={[
                { value: '', label: '全部结果' },
                { value: 'success', label: '成功' },
                { value: 'failure', label: '失败' },
              ]}
              onChange={(event) => setOutcome(event.target.value as AuditOutcome)}
            />
          </Field>
          <Field label="资源键前缀" htmlFor="governance-audit-resource">
            <Input
              id="governance-audit-resource"
              value={resourceKeyPrefix}
              maxLength={512}
              autoComplete="off"
              onChange={(event) => setResourceKeyPrefix(event.target.value)}
            />
          </Field>
        </div>
      </Toolbar>
      {filterError && (
        <div className="inline-notice">
          <Alert tone="error" title={filterError} />
        </div>
      )}
      <div className="inline-actions governance-audit-search">
        <Button variant="primary" loading={audit.isFetching} onClick={submit}>
          查询审计
        </Button>
        <span className="evia-field-hint">半开区间 [开始, 结束)，最长 31 天，按时间倒序。</span>
      </div>
      {audit.isError ? (
        <Alert
          tone="error"
          title="无法读取审计记录"
          action={<Button onClick={() => void audit.refetch()}>重试</Button>}
        >
          {audit.error instanceof ApiError ? audit.error.message : '请稍后再试。'}
        </Alert>
      ) : (
        <>
          <DataTable
            caption="项目审计记录"
            columns={columns}
            rows={audit.data?.items ?? []}
            rowKey={(row) => row.id}
            minWidth={1040}
            loading={audit.isFetching}
            empty={<EmptyPanel description="所选范围内没有审计记录。" />}
          />
          <PaginationBar
            page={cursors.length + 1}
            count={audit.data?.items.length ?? 0}
            hasNext={!!audit.data?.next_cursor}
            busy={audit.isFetching}
            onPrevious={() => setCursors((current) => current.slice(0, -1))}
            onNext={() => {
              if (audit.data?.next_cursor)
                setCursors((current) => [...current, audit.data!.next_cursor!]);
            }}
          />
        </>
      )}
    </DataSurface>
  );
}
