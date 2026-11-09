import { useEffect, useId, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, type DashboardWidget, type SavedAnalysisVersion } from '@evia/api-client';
import {
  Button,
  Field,
  Input,
  Modal,
  Select,
  LoadingPanel,
  PaginationBar,
} from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import { CatalogError } from '../catalog-helpers';
import { savedDefinitionSummary } from '../saved-analysis-model';

export function DashboardWidgetPicker({
  context,
  open,
  onClose,
  onAdd,
  onDenied,
}: {
  context: CatalogPageProps;
  open: boolean;
  onClose: () => void;
  onAdd: (version: SavedAnalysisVersion, title: string) => void;
  onDenied: (error: Error) => void;
}) {
  const id = useId(),
    { project, environment, session } = context;
  const [cursors, setCursors] = useState<string[]>([]),
    [analysisID, setAnalysisID] = useState(''),
    [version, setVersion] = useState(''),
    [title, setTitle] = useState('');
  const [versionCursors, setVersionCursors] = useState<string[]>([]);
  const scope = ['dashboards', project.id, environment, session.user_id, session.csrf_token];
  const list = useQuery({
    queryKey: [...scope, 'list', cursors.at(-1)],
    queryFn: ({ signal }) =>
      api.listSavedAnalyses(project.id, { environment, cursor: cursors.at(-1), limit: 20 }, signal),
    enabled: open,
  });
  const versions = useQuery({
    queryKey: [...scope, 'versions', analysisID, versionCursors.at(-1)],
    queryFn: ({ signal }) =>
      api.listSavedAnalysisVersions(
        project.id,
        analysisID,
        { limit: 20, cursor: versionCursors.at(-1) },
        signal,
      ),
    enabled: open && !!analysisID,
  });
  const selected = versions.data?.items.find((item) => item.version === Number(version));
  useEffect(() => {
    if (open) {
      setAnalysisID('');
      setVersion('');
      setTitle('');
      setCursors([]);
      setVersionCursors([]);
    }
  }, [open]);
  useEffect(() => {
    for (const error of [list.error, versions.error])
      if (error && 'status' in error && [401, 403].includes(Number(error.status))) onDenied(error);
  }, [list.error, versions.error, onDenied]);
  return (
    <Modal
      open={open}
      title="添加分析组件"
      onRequestClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>取消</Button>
          <Button variant="primary" type="submit" form={`${id}-form`} disabled={!selected}>
            添加组件
          </Button>
        </>
      }
    >
      <form
        id={`${id}-form`}
        className="section-stack"
        onSubmit={(e) => {
          e.preventDefault();
          if (selected && title.trim()) {
            onAdd(selected, title.trim());
            onClose();
          }
        }}
      >
        <CatalogError error={list.error} retry={() => void list.refetch()} />
        {list.isPending ? (
          <LoadingPanel label="正在读取保存分析" />
        ) : (
          <Field label="保存的分析" htmlFor={`${id}-analysis`}>
            <Select
              id={`${id}-analysis`}
              value={analysisID}
              onChange={(e) => {
                setAnalysisID(e.target.value);
                setVersion('');
                setVersionCursors([]);
                setTitle('');
              }}
              options={[
                { value: '', label: '选择已发布的分析' },
                ...(list.data?.items ?? [])
                  .filter((item) => item.published_version > 0)
                  .map((item) => ({
                    value: item.analysis_id,
                    label: `${item.title}，最新 v${item.published_version}`,
                  })),
              ]}
            />
          </Field>
        )}
        {!list.isPending &&
          !(list.data?.items ?? []).some((item) => item.published_version > 0) && (
            <p>本页暂无已发布分析。可继续翻页，或先在保存的分析中创建并发布。</p>
          )}
        {(cursors.length > 0 || list.data?.next_cursor) && (
          <PaginationBar
            page={cursors.length + 1}
            count={list.data?.items.length ?? 0}
            hasNext={!!list.data?.next_cursor}
            busy={list.isFetching}
            onPrevious={() => setCursors((c) => c.slice(0, -1))}
            onNext={() => {
              if (list.data?.next_cursor) setCursors((c) => [...c, list.data!.next_cursor!]);
            }}
          />
        )}
        {analysisID && (
          <>
            <CatalogError error={versions.error} retry={() => void versions.refetch()} />
            <Field label="固定分析版本" htmlFor={`${id}-version`}>
              <Select
                id={`${id}-version`}
                disabled={versions.isFetching}
                value={version}
                options={[
                  { value: '', label: '选择不可变版本' },
                  ...(versions.data?.items ?? []).map((item) => ({
                    value: String(item.version),
                    label: `v${item.version}，${item.definition.title}`,
                  })),
                ]}
                onChange={(e) => {
                  setVersion(e.target.value);
                  const item = versions.data?.items.find(
                    (v) => v.version === Number(e.target.value),
                  );
                  setTitle(item?.definition.title ?? '');
                }}
              />
            </Field>
            {(versionCursors.length > 0 || versions.data?.next_cursor) && (
              <PaginationBar
                page={versionCursors.length + 1}
                count={versions.data?.items.length ?? 0}
                hasNext={!!versions.data?.next_cursor}
                busy={versions.isFetching}
                onPrevious={() => setVersionCursors((c) => c.slice(0, -1))}
                onNext={() => {
                  if (versions.data?.next_cursor)
                    setVersionCursors((c) => [...c, versions.data!.next_cursor!]);
                }}
              />
            )}
          </>
        )}
        {selected && (
          <>
            <p className="field-help">{savedDefinitionSummary(selected.definition)}</p>
            <Field label="组件标题" htmlFor={`${id}-title`}>
              <Input
                id={`${id}-title`}
                value={title}
                maxLength={256}
                required
                onChange={(e) => setTitle(e.target.value)}
              />
            </Field>
          </>
        )}
        <p className="field-help">
          固定发布版本，后续修改分析草稿不会改变该组件。每个组件可独立选择分析和版本。
        </p>
      </form>
    </Modal>
  );
}
