import { useEffect, useId, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, type MetricRecord } from '@evia/api-client';
import {
  Button,
  Field,
  Input,
  Modal,
  Select,
  Textarea,
  LoadingPanel,
  PaginationBar,
} from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import { CatalogError } from '../catalog-helpers';
import { savedDefinitionSummary } from '../saved-analysis-model';
import { maxTextCardContentRunes } from './model';
import type { DashboardWidgetSource } from './types';

type SourceKind = 'analysis' | 'metric' | 'text';

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
  onAdd: (source: DashboardWidgetSource, title: string) => void;
  onDenied: (error: Error) => void;
}) {
  const id = useId(),
    { project, environment, session } = context;
  const [sourceKind, setSourceKind] = useState<SourceKind>('analysis'),
    [cursors, setCursors] = useState<string[]>([]),
    [analysisID, setAnalysisID] = useState(''),
    [version, setVersion] = useState(''),
    [metricID, setMetricID] = useState(''),
    [textContent, setTextContent] = useState(''),
    [title, setTitle] = useState('');
  const [versionCursors, setVersionCursors] = useState<string[]>([]);
  const scope = ['dashboards', project.id, environment, session.user_id, session.csrf_token];

  const analysisList = useQuery({
    queryKey: [...scope, 'analysis-list', cursors.at(-1)],
    queryFn: ({ signal }) =>
      api.listSavedAnalyses(project.id, { environment, cursor: cursors.at(-1), limit: 20 }, signal),
    enabled: open && sourceKind === 'analysis',
  });
  const analysisVersions = useQuery({
    queryKey: [...scope, 'analysis-versions', analysisID, versionCursors.at(-1)],
    queryFn: ({ signal }) =>
      api.listSavedAnalysisVersions(
        project.id,
        analysisID,
        { limit: 20, cursor: versionCursors.at(-1) },
        signal,
      ),
    enabled: open && sourceKind === 'analysis' && !!analysisID,
  });
  const metricList = useQuery({
    queryKey: [...scope, 'metric-list', cursors.at(-1)],
    queryFn: ({ signal }) =>
      api.listMetrics(project.id, { environment, cursor: cursors.at(-1), limit: 20 }, signal),
    enabled: open && sourceKind === 'metric',
  });
  const metricVersions = useQuery({
    queryKey: [...scope, 'metric-versions', metricID, versionCursors.at(-1)],
    queryFn: ({ signal }) =>
      api.listMetricVersions(
        project.id,
        metricID,
        { limit: 20, cursor: versionCursors.at(-1) },
        signal,
      ),
    enabled: open && sourceKind === 'metric' && !!metricID,
  });

  const selectedAnalysis = analysisVersions.data?.items.find(
    (item) => item.version === Number(version),
  );
  const selectedMetricRecord: MetricRecord | undefined = metricList.data?.items.find(
    (item) => item.metric_id === metricID,
  );
  const selectedMetricVersion = metricVersions.data?.items.find(
    (item) => item.version === Number(version),
  );
  const textRunes = [...textContent].length;
  const ready =
    sourceKind === 'text'
      ? title.trim().length > 0 &&
        textContent.trim().length > 0 &&
        textRunes <= maxTextCardContentRunes
      : sourceKind === 'analysis'
        ? !!selectedAnalysis && title.trim().length > 0
        : !!selectedMetricRecord && !!selectedMetricVersion && title.trim().length > 0;

  useEffect(() => {
    if (open) {
      setSourceKind('analysis');
      setAnalysisID('');
      setMetricID('');
      setVersion('');
      setTextContent('');
      setTitle('');
      setCursors([]);
      setVersionCursors([]);
    }
  }, [open]);
  function switchSource(kind: SourceKind) {
    setSourceKind(kind);
    setAnalysisID('');
    setMetricID('');
    setVersion('');
    setTextContent('');
    setTitle('');
    setCursors([]);
    setVersionCursors([]);
  }
  useEffect(() => {
    const errors =
      sourceKind === 'analysis'
        ? [analysisList.error, analysisVersions.error]
        : [metricList.error, metricVersions.error];
    for (const error of errors)
      if (error && 'status' in error && [401, 403].includes(Number(error.status))) onDenied(error);
  }, [
    sourceKind,
    analysisList.error,
    analysisVersions.error,
    metricList.error,
    metricVersions.error,
    onDenied,
  ]);

  const list = sourceKind === 'analysis' ? analysisList : metricList;
  const versions = sourceKind === 'analysis' ? analysisVersions : metricVersions;

  return (
    <Modal
      open={open}
      title="添加看板组件"
      onRequestClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>取消</Button>
          <Button variant="primary" type="submit" form={`${id}-form`} disabled={!ready}>
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
          if (!ready) return;
          if (sourceKind === 'text') {
            onAdd({ kind: 'text', content: textContent.trim() }, title.trim());
          } else if (sourceKind === 'analysis' && selectedAnalysis) {
            onAdd({ kind: 'analysis', analysis: selectedAnalysis }, title.trim());
          } else if (sourceKind === 'metric' && selectedMetricRecord && selectedMetricVersion) {
            onAdd(
              { kind: 'metric', metric: selectedMetricRecord, version: selectedMetricVersion },
              title.trim(),
            );
          }
          onClose();
        }}
      >
        <Field label="组件来源" htmlFor={`${id}-source`}>
          <Select
            id={`${id}-source`}
            value={sourceKind}
            onChange={(e) => switchSource(e.target.value as SourceKind)}
            options={[
              { value: 'analysis', label: '已发布保存分析' },
              { value: 'metric', label: '已发布独立指标' },
              { value: 'text', label: 'Markdown 文本卡（说明/注释，不产生查询）' },
            ]}
          />
        </Field>

        {sourceKind === 'text' ? (
          <>
            <Field label="文本卡内容（Markdown 子集）" htmlFor={`${id}-text-content`}>
              <Textarea
                id={`${id}-text-content`}
                rows={8}
                maxLength={maxTextCardContentRunes}
                required
                value={textContent}
                onChange={(e) => setTextContent(e.target.value)}
              />
            </Field>
            <p className="field-help">
              {textRunes}/{maxTextCardContentRunes}{' '}
              字符；支持标题、列表、引用、围栏代码块、加粗/斜体/行内代码与 http(s) 链接，不支持原始
              HTML 与图片。
            </p>
            <Field label="组件标题" htmlFor={`${id}-text-title`}>
              <Input
                id={`${id}-text-title`}
                value={title}
                maxLength={256}
                required
                onChange={(e) => setTitle(e.target.value)}
              />
            </Field>
            <p className="field-help">文本卡仅用于展示说明，不固定数据来源，运行时不产生查询。</p>
          </>
        ) : (
          <>
            <CatalogError error={list.error} retry={() => void list.refetch()} />
            {list.isPending ? (
              <LoadingPanel
                label={sourceKind === 'analysis' ? '正在读取保存分析' : '正在读取指标'}
              />
            ) : sourceKind === 'analysis' ? (
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
                    ...(analysisList.data?.items ?? [])
                      .filter((item) => item.published_version > 0)
                      .map((item) => ({
                        value: item.analysis_id,
                        label: `${item.title}，最新 v${item.published_version}`,
                      })),
                  ]}
                />
              </Field>
            ) : (
              <Field label="独立指标" htmlFor={`${id}-metric`}>
                <Select
                  id={`${id}-metric`}
                  value={metricID}
                  onChange={(e) => {
                    setMetricID(e.target.value);
                    setVersion('');
                    setVersionCursors([]);
                    setTitle('');
                  }}
                  options={[
                    { value: '', label: '选择已发布的指标' },
                    ...(metricList.data?.items ?? [])
                      .filter((item) => item.published_version > 0)
                      .map((item) => ({
                        value: item.metric_id,
                        label: `${item.title}，最新 v${item.published_version}`,
                      })),
                  ]}
                />
              </Field>
            )}
            {!list.isPending &&
              (sourceKind === 'analysis'
                ? !(analysisList.data?.items ?? []).some((item) => item.published_version > 0)
                : !(metricList.data?.items ?? []).some((item) => item.published_version > 0)) && (
                <p className="field-help">
                  {sourceKind === 'analysis'
                    ? '本页暂无已发布分析，可继续翻页或先在保存的分析中发布。'
                    : '本页暂无已发布指标，可继续翻页或先在独立指标中发布。'}
                </p>
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

            {((sourceKind === 'analysis' && analysisID) ||
              (sourceKind === 'metric' && metricID)) && (
              <>
                <CatalogError error={versions.error} retry={() => void versions.refetch()} />
                <Field
                  label={sourceKind === 'analysis' ? '固定分析版本' : '固定指标版本'}
                  htmlFor={`${id}-version`}
                >
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
                      if (sourceKind === 'analysis') {
                        const item = analysisVersions.data?.items.find(
                          (v) => v.version === Number(e.target.value),
                        );
                        setTitle(item?.definition.title ?? '');
                      } else {
                        const item = metricVersions.data?.items.find(
                          (v) => v.version === Number(e.target.value),
                        );
                        setTitle(item?.definition.title ?? '');
                      }
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
            {sourceKind === 'analysis' && selectedAnalysis && (
              <p className="field-help">{savedDefinitionSummary(selectedAnalysis.definition)}</p>
            )}
            {selectedMetricVersion && (
              <p className="field-help">
                指标 {selectedMetricVersion.definition.title}（
                {selectedMetricVersion.definition.unit}
                ）， 运行时按固定口径生成趋势查询。
              </p>
            )}
            {version && (
              <Field label="组件标题" htmlFor={`${id}-title`}>
                <Input
                  id={`${id}-title`}
                  value={title}
                  maxLength={256}
                  required
                  onChange={(e) => setTitle(e.target.value)}
                />
              </Field>
            )}
            <p className="field-help">
              固定发布版本，后续修改草稿不会改变该组件。每个组件只能选择分析、指标或文本卡中的一种来源。
            </p>
          </>
        )}
      </form>
    </Modal>
  );
}
