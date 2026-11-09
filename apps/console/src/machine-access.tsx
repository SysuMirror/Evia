import { useCallback, useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiError,
  type Environment,
  type MachineIssued,
  type MachinePrincipal,
  type Project,
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
  LoadingPanel,
  Modal,
  PageHeading,
  PaginationBar,
  Toolbar,
  type DataTableColumn,
} from '@evia/design-system';
import { useUnsavedWork } from './use-unsaved-work';
import { MachineDetail } from './machine-access/detail';
import { MachineFailure } from './machine-access/failure';
import { MachineIssuance, type IssuanceIntent } from './machine-access/issuance';
import { MachineRevocation, type RevocationTarget } from './machine-access/revocation';
import { MachineSecret } from './machine-access/secret';
import { dateLabel, environmentLabels } from './machine-access/model';
import { useMachineRequest } from './machine-access/use-request';
import './machine-access/styles.css';

type PageProps = { project: Project; session: Session; environment: Environment };
export function MachineAccessPage(props: PageProps) {
  if (!props.project.permissions.includes('machine_principals.manage'))
    return <ErrorPanel message="只有项目所有者可以管理机器访问。" />;
  return (
    <MachineWorkspace
      key={`${props.project.id}:${props.environment}:${props.session.user_id}:${props.session.csrf_token}`}
      {...props}
    />
  );
}

function MachineWorkspace({ project, session, environment }: PageProps) {
  const cache = useQueryClient();
  const [cursors, setCursors] = useState<string[]>([]);
  const [selectedID, setSelectedID] = useState<string>();
  const [issuance, setIssuance] = useState<IssuanceIntent>();
  const [issuanceOpen, setIssuanceOpen] = useState(false);
  const [issuanceDirty, setIssuanceDirty] = useState(false);
  const [revocation, setRevocation] = useState<RevocationTarget>();
  const [issued, setIssued] = useState<{ principal: MachinePrincipal; secret: string }>();
  const [secretSaved, setSecretSaved] = useState(false);
  const [missingSecret, setMissingSecret] = useState<MachinePrincipal>();
  const queryPrefix = ['machine-access', project.id, session.user_id] as const;
  const requests = useMachineRequest(
    project,
    session,
    `${project.id}:${environment}:${session.user_id}:${session.csrf_token}`,
  );
  const list = useQuery({
    queryKey: [...queryPrefix, 'list', cursors.at(-1) ?? ''],
    queryFn: ({ signal }) =>
      api.listMachinePrincipals(project.id, { limit: 20, cursor: cursors.at(-1) }, signal),
  });
  const detail = useQuery({
    queryKey: [...queryPrefix, 'detail', selectedID ?? ''],
    queryFn: ({ signal }) => api.getMachinePrincipal(project.id, selectedID!, signal),
    enabled: !!selectedID,
    staleTime: 0,
  });
  const resetLocal = () => {
    setIssuance(undefined);
    setIssuanceOpen(false);
    setIssuanceDirty(false);
    setIssued(undefined);
    setSecretSaved(false);
    setRevocation(undefined);
  };
  const guard = useUnsavedWork({
    dirty: issuanceDirty || (!!issued && !secretSaved),
    busy: !!requests.busy,
    onDiscard: resetLocal,
  });
  const setDirty = useCallback((value: boolean) => setIssuanceDirty(value), []);
  const refresh = () => {
    void cache.invalidateQueries({ queryKey: queryPrefix });
  };
  const receive = (result: MachineIssued) => {
    // Issued response objects never enter QueryClient or mutation caches.
    // Queries fetch public metadata separately after the issuance completes.
    setIssuance(undefined);
    setIssuanceOpen(false);
    setIssuanceDirty(false);
    setSecretSaved(false);
    if (
      result.secret_available &&
      typeof result.secret_once === 'string' &&
      result.secret_once.length > 0
    ) {
      setMissingSecret(undefined);
      setSelectedID(result.principal.id);
      setIssued({ principal: result.principal, secret: result.secret_once });
    } else {
      setSelectedID(undefined);
      setMissingSecret(result.principal);
    }
    refresh();
  };
  const startRotation = (principal: MachinePrincipal) => {
    if (requests.busy) return;
    if (!issuance) setIssuance({ kind: 'rotate', principal });
    setIssuanceOpen(true);
  };
  const columns: DataTableColumn<MachinePrincipal>[] = [
    {
      key: 'name',
      header: '机器名称',
      render: (row) => (
        <Button className="project-link" variant="link" onClick={() => setSelectedID(row.id)}>
          {row.name}
        </Button>
      ),
    },
    {
      key: 'environment',
      header: '授权环境',
      render: (row) => <Badge>{environmentLabels[row.environment]}</Badge>,
    },
    { key: 'permissions', header: '权限', render: (row) => `${row.permissions.length} 项` },
    {
      key: 'status',
      header: '状态',
      render: (row) => (
        <Badge tone={row.revoked_at ? 'neutral' : 'success'}>
          {row.revoked_at ? '已撤销' : '启用'}
        </Badge>
      ),
    },
    { key: 'created', header: '创建时间', render: (row) => dateLabel(row.created_at) },
  ];
  const deniedRead = [list.error, detail.error].find(
    (error) => error instanceof ApiError && [401, 403].includes(error.status),
  );
  useEffect(() => {
    if (deniedRead) requests.deny(deniedRead);
  }, [deniedRead, requests.deny]);
  if (requests.denied || deniedRead)
    return <MachineFailure error={requests.denied ?? deniedRead!} />;
  const currentIntent =
    issuance?.kind === 'rotate' && detail.data?.principal.id === issuance.principal.id
      ? { kind: 'rotate' as const, principal: detail.data.principal }
      : issuance;
  return (
    <>
      <PageHeading
        title="机器访问"
        action={
          <Button
            variant="primary"
            disabled={!!requests.busy}
            onClick={() => {
              if (!issuance) setIssuance({ kind: 'create' });
              setIssuanceOpen(true);
            }}
          >
            {issuance ? '继续凭据操作' : '创建机器'}
          </Button>
        }
      />
      {missingSecret && (
        <div className="inline-notice">
          <Alert tone="warning" title="凭据记录已确认，未收到可用密钥">
            <p>
              {missingSecret.name}，{environmentLabels[missingSecret.environment]}
              。原密钥无法恢复，请显式轮换并保存新密钥。
            </p>
            <Button
              disabled={!!requests.busy || !!missingSecret.revoked_at}
              onClick={() => {
                setSelectedID(missingSecret.id);
                startRotation(
                  detail.data?.principal.id === missingSecret.id
                    ? detail.data.principal
                    : missingSecret,
                );
              }}
            >
              重新轮换凭据
            </Button>
          </Alert>
        </div>
      )}
      {issuance && !issuanceOpen && (
        <div className="inline-notice">
          <Alert tone="info" title="凭据操作已保留">
            <Button onClick={() => setIssuanceOpen(true)}>
              继续{issuance.kind === 'create' ? '创建机器' : '轮换凭据'}
            </Button>
          </Alert>
        </div>
      )}
      <DataSurface label="机器访问">
        <Toolbar
          label="机器列表范围"
          actions={
            <Button disabled={list.isFetching || !!requests.busy} onClick={refresh}>
              刷新
            </Button>
          }
        >
          <Badge>全部环境</Badge>
          <span className="evia-field-hint">按机器的授权环境管理访问</span>
        </Toolbar>
        {list.isPending ? (
          <LoadingPanel label="正在加载机器" />
        ) : list.isError ? (
          <MachineFailure error={list.error} retry={() => void list.refetch()} />
        ) : (
          <>
            <DataTable
              caption="机器访问列表"
              columns={columns}
              rows={list.data.items}
              rowKey={(row) => row.id}
              minWidth={720}
              loading={list.isFetching}
              empty={
                <EmptyPanel description="还没有机器，为自动化任务创建机器，选择环境与所需权限后签发凭据。" />
              }
            />
            <PaginationBar
              page={cursors.length + 1}
              count={list.data.items.length}
              hasNext={!!list.data.next_cursor}
              busy={list.isFetching || !!requests.busy}
              onPrevious={() => setCursors((current) => current.slice(0, -1))}
              onNext={() => {
                if (list.data.next_cursor)
                  setCursors((current) => [...current, list.data.next_cursor!]);
              }}
            />
          </>
        )}
      </DataSurface>
      <Modal
        size="wide"
        open={!!selectedID}
        title={detail.data ? `机器：${detail.data.principal.name}` : '机器详情'}
        onRequestClose={() => {
          if (!requests.busy) setSelectedID(undefined);
        }}
        busy={!!requests.busy}
        footer={
          <Button disabled={!!requests.busy} onClick={() => setSelectedID(undefined)}>
            关闭详情
          </Button>
        }
      >
        {detail.isPending ? (
          <LoadingPanel label="正在加载机器详情" />
        ) : detail.isError ? (
          <MachineFailure error={detail.error} retry={() => void detail.refetch()} />
        ) : (
          detail.data && (
            <MachineDetail
              detail={detail.data}
              busy={!!requests.busy || !!issuance}
              onRotate={() => startRotation(detail.data.principal)}
              onRevoke={() => setRevocation({ principal: detail.data.principal })}
              onRevokeCredential={(credential) =>
                setRevocation({ principal: detail.data.principal, credential })
              }
            />
          )
        )}
      </Modal>
      {currentIntent && (
        <MachineIssuance
          open={issuanceOpen}
          intent={currentIntent}
          projectID={project.id}
          environment={environment}
          session={session}
          busy={requests.busy}
          perform={requests.perform}
          onClose={() => setIssuanceOpen(false)}
          onDiscard={() =>
            guard.requestLeave(() => {
              setIssuance(undefined);
              setIssuanceOpen(false);
              setIssuanceDirty(false);
            })
          }
          onDirtyChange={setDirty}
          onIssued={receive}
          onConflict={refresh}
        />
      )}
      {revocation && (
        <MachineRevocation
          target={revocation}
          projectID={project.id}
          session={session}
          busy={requests.busy}
          perform={requests.perform}
          onClose={() => setRevocation(undefined)}
          onComplete={() => {
            setRevocation(undefined);
            refresh();
          }}
        />
      )}
      {issued && (
        <MachineSecret
          issued={issued}
          saved={secretSaved}
          onSaved={setSecretSaved}
          onClose={() =>
            guard.requestLeave(() => {
              setIssued(undefined);
              setSecretSaved(false);
            })
          }
        />
      )}
      {guard.confirmation}
    </>
  );
}
