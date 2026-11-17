import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, type Dashboard, type DashboardWidget, type Environment } from '@evia/api-client';
import { Field, LoadingPanel, Select } from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import { CatalogError } from '../catalog-helpers';
import { buildRebindings, executableWidgets, type CrossCopySelection } from './model';
import type { DashboardCopyRequest } from './types';

const ENVIRONMENTS: Environment[] = ['development', 'staging', 'production'];

type CrossEnvironmentCopyFormProps = {
  dashboard: Dashboard;
  sourceEnvironment: Environment;
  context: CatalogPageProps;
  onChange: (request: DashboardCopyRequest | null) => void;
};

function RebindingRow({
  widget,
  targetEnvironment,
  selection,
  onSelect,
  context,
}: {
  widget: DashboardWidget;
  targetEnvironment: Environment;
  selection?: CrossCopySelection;
  onSelect: (selection: CrossCopySelection) => void;
  context: CatalogPageProps;
}) {
  const { project, session } = context;
  const isMetric = !!widget.metric_ref;
  const scope = [
    'dashboards',
    'cross-copy',
    project.id,
    targetEnvironment,
    session.user_id,
    session.csrf_token,
    isMetric ? 'metric' : 'analysis',
  ];
  const analyses = useQuery({
    queryKey: [...scope, 'analyses'],
    queryFn: ({ signal }) =>
      api.listSavedAnalyses(
        project.id,
        { environment: targetEnvironment, view: 'active', limit: 50 },
        signal,
      ),
    enabled: !isMetric,
  });
  const metrics = useQuery({
    queryKey: [...scope, 'metrics'],
    queryFn: ({ signal }) =>
      api.listMetrics(
        project.id,
        { environment: targetEnvironment, view: 'active', limit: 50 },
        signal,
      ),
    enabled: isMetric,
  });
  const resourceID = selection?.resourceID ?? '';
  const analysisVersions = useQuery({
    queryKey: [...scope, 'analysis-versions', resourceID],
    queryFn: ({ signal }) =>
      api.listSavedAnalysisVersions(project.id, resourceID, { limit: 50 }, signal),
    enabled: !isMetric && !!resourceID,
  });
  const metricVersions = useQuery({
    queryKey: [...scope, 'metric-versions', resourceID],
    queryFn: ({ signal }) => api.listMetricVersions(project.id, resourceID, { limit: 50 }, signal),
    enabled: isMetric && !!resourceID,
  });
  const resourcesPending = isMetric ? metrics.isPending : analyses.isPending;
  const versionsPending = isMetric ? metricVersions.isPending : analysisVersions.isPending;
  const resourceOptions = useMemo(() => {
    if (isMetric) {
      return (metrics.data?.items ?? []).map((item) => ({
        value: item.metric_id,
        label: item.title,
      }));
    }
    return (analyses.data?.items ?? []).map((item) => ({
      value: item.analysis_id,
      label: item.title,
    }));
  }, [analyses.data, metrics.data, isMetric]);
  const versionOptions = useMemo(() => {
    const items = isMetric ? metricVersions.data?.items : analysisVersions.data?.items;
    return (items ?? []).map((item) => ({
      value: String(item.version),
      label: `v${item.version}`,
    }));
  }, [analysisVersions.data, metricVersions.data, isMetric]);
  return (
    <div className="cross-copy-row">
      <p className="field-help">
        {widget.title}（{isMetric ? '指标' : '保存分析'}）
      </p>
      <Select
        value={resourceID}
        disabled={resourcesPending}
        options={[
          { value: '', label: resourcesPending ? '读取目录中…' : '选择目标资源' },
          ...resourceOptions,
        ]}
        onChange={(event) => onSelect({ resourceID: event.target.value, version: '' })}
      />
      <Select
        value={selection?.version ?? ''}
        disabled={!resourceID || versionsPending}
        options={[
          {
            value: '',
            label: !resourceID
              ? '请先选择资源'
              : versionsPending
                ? '读取版本中…'
                : '选择已发布版本',
          },
          ...versionOptions,
        ]}
        onChange={(event) => onSelect({ resourceID, version: event.target.value })}
      />
    </div>
  );
}

/**
 * Cross-environment copy form. Every executable source widget must be rebound
 * to an explicitly selected, published target-environment version; presentation
 * text cards carry over without an entry. The form reports a complete request
 * upward only when the title is valid and every executable widget is resolved.
 */
export function CrossEnvironmentCopyForm({
  dashboard,
  sourceEnvironment,
  context,
  onChange,
}: CrossEnvironmentCopyFormProps) {
  const { project, session } = context;
  const targetEnvironments = useMemo(
    () => ENVIRONMENTS.filter((value) => value !== sourceEnvironment),
    [sourceEnvironment],
  );
  const [targetEnvironment, setTargetEnvironment] = useState<Environment>(targetEnvironments[0]!);
  const [title, setTitle] = useState(`${dashboard.title} 副本`);
  const [folderID, setFolderID] = useState('');
  const [selections, setSelections] = useState<Record<string, CrossCopySelection>>({});

  const draft = useQuery({
    queryKey: [
      'dashboards',
      'cross-copy',
      project.id,
      dashboard.dashboard_id,
      'draft',
      session.user_id,
      session.csrf_token,
    ],
    queryFn: ({ signal }) => api.getDashboardDraft(project.id, dashboard.dashboard_id, signal),
  });
  const folders = useQuery({
    queryKey: ['dashboards', 'cross-copy', project.id, targetEnvironment, 'folders'],
    queryFn: ({ signal }) =>
      api.listDashboardFolders(project.id, { environment: targetEnvironment, limit: 100 }, signal),
  });

  const widgets = draft.data?.definition.widgets ?? [];
  const executable = executableWidgets(widgets);
  const textCards = widgets.filter((widget) => widget.text_card);

  // Switching target environment invalidates every prior selection because the
  // catalogs and versions are environment-scoped resources.
  function changeTargetEnvironment(value: Environment) {
    setTargetEnvironment(value);
    setSelections({});
    setFolderID('');
  }

  useEffect(() => {
    const trimmed = title.trim();
    const titleValid = trimmed.length >= 1 && trimmed.length <= 128;
    const rebindings = buildRebindings(widgets, selections);
    if (!titleValid || !rebindings) {
      onChange(null);
      return;
    }
    onChange({
      title: trimmed,
      folderID,
      targetEnvironment,
      rebindings,
    });
  }, [title, folderID, targetEnvironment, selections, widgets, onChange]);

  if (draft.isPending) return <LoadingPanel label="读取源看板草稿" />;
  return (
    <div className="section-stack">
      <CatalogError error={draft.error} retry={() => void draft.refetch()} />
      <Field label="目标环境" htmlFor="cross-copy-env">
        <Select
          value={targetEnvironment}
          options={targetEnvironments.map((value) => ({ value, label: value }))}
          onChange={(event) => changeTargetEnvironment(event.target.value as Environment)}
        />
      </Field>
      <Field label="新看板标题" htmlFor="cross-copy-title" hint="1 至 128 个字符。">
        <input
          id="cross-copy-title"
          className="evia-input"
          value={title}
          maxLength={128}
          onChange={(event) => setTitle(event.target.value)}
        />
      </Field>
      <Field label="目标文件夹" htmlFor="cross-copy-folder">
        <Select
          value={folderID}
          options={[
            { value: '', label: '根级（不属于任何文件夹）' },
            ...(folders.data?.items ?? []).map((folder) => ({
              value: folder.folder_id,
              label: `${'　'.repeat(folder.depth - 1)}${folder.title}`,
            })),
          ]}
          onChange={(event) => setFolderID(event.target.value)}
        />
      </Field>
      <div className="cross-copy-widgets">
        <p className="field-help">
          每个可执行组件必须重绑定到目标环境中已发布的同种类版本；共 {executable.length}{' '}
          个待重绑定。
        </p>
        {executable.map((widget) => (
          <RebindingRow
            key={widget.widget_id}
            widget={widget}
            targetEnvironment={targetEnvironment}
            context={context}
            selection={selections[widget.widget_id]}
            onSelect={(selection) =>
              setSelections((previous) => ({ ...previous, [widget.widget_id]: selection }))
            }
          />
        ))}
        {textCards.length > 0 && (
          <p className="field-help">{textCards.length} 个文本卡将原样沿用，无需重绑定。</p>
        )}
      </div>
    </div>
  );
}
