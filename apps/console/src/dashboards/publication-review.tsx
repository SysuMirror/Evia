import type { DashboardValidation } from '@evia/api-client';
import { Alert, Badge, Button, Checkbox, CodeBlock, DataTable, Modal } from '@evia/design-system';
import { CatalogError } from '../catalog-helpers';
const categories: Record<string, string> = {
  definition: '初始定义',
  parameters: '运行参数',
  widgets: '组件',
  dependencies: '固定组件依赖',
  layout: '组件布局',
  metadata: '标题与描述',
  kind: '分析类型',
  event_versions: '固定分析版本',
  filter: '筛选条件',
  subject: '统计主体',
  metrics: '指标',
  group_by: '分组',
  interval: '时间粒度',
  funnel: '漏斗',
  limit: '结果上限',
  visualization: '展示方式',
};
type PublicationReviewProps = {
  open: boolean;
  validation?: DashboardValidation;
  busy: string;
  canWrite: boolean;
  acknowledged: boolean;
  dirty: boolean;
  retryPending: boolean;
  error: Error | null;
  onClose: () => void;
  onPublish: () => void;
  onAcknowledge: (acknowledged: boolean) => void;
};
export function DashboardPublicationReview({
  open,
  validation,
  busy,
  canWrite,
  acknowledged,
  dirty,
  retryPending,
  error,
  onClose,
  onPublish,
  onAcknowledge,
}: PublicationReviewProps) {
  return (
    <Modal
      open={open}
      title="发布前校验"
      onRequestClose={() => {
        if (!busy) onClose();
      }}
      busy={!!busy}
      footer={
        <>
          <Button disabled={!!busy} onClick={() => onClose()}>
            返回草稿
          </Button>
          {canWrite && (
            <Button
              variant="primary"
              loading={busy === '发布版本'}
              disabled={!!busy || !acknowledged || !validation || dirty}
              onClick={onPublish}
            >
              {retryPending ? '重试发布版本' : '发布不可变版本'}
            </Button>
          )}
        </>
      }
    >
      <CatalogError error={error} />
      {validation && (
        <div className="section-stack dashboard-validation">
          <div className="inline-actions">
            <Badge tone="success">校验通过</Badge>
            <span>
              草稿 r{validation.draft_revision}，基于
              {validation.published_version
                ? `已发布 v${validation.published_version}`
                : '首次发布'}
            </span>
          </div>
          <p className="field-help">
            校验未执行查询。发布后配置与依赖不可修改，实际运行仍受权限与查询预算约束。
          </p>
          <h3>固定组件依赖</h3>
          <DataTable
            caption="本次发布的精确组件来源版本"
            rows={validation.dependencies.analyses}
            columns={[
              {
                key: 'event',
                header: '组件',
                render: (dependency) =>
                  validation.normalized_definition.widgets.find(
                    (w) => w.widget_id === dependency.widget_id,
                  )?.title ?? '组件',
              },
              {
                key: 'version',
                header: '固定版本',
                render: (dependency) =>
                  dependency.metric_id
                    ? `指标 v${dependency.metric_version}`
                    : `分析 v${dependency.analysis_version}`,
              },
              {
                key: 'hash',
                header: '定义摘要',
                render: (dependency) => (
                  <details>
                    <summary>查看摘要</summary>
                    <code>{dependency.definition_hash}</code>
                  </details>
                ),
              },
            ]}
            rowKey={(dependency) => dependency.widget_id}
          />
          <h3>相对上次发布的语义变化</h3>
          {validation.diff.length ? (
            <DataTable
              caption="发布前语义差异"
              rows={validation.diff}
              columns={[
                {
                  key: 'category',
                  header: '变更',
                  render: (change) => categories[change.category] ?? change.category,
                },
                { key: 'path', header: '位置', render: (change) => <code>{change.path}</code> },
                {
                  key: 'values',
                  header: '内容',
                  render: (change) => (
                    <details>
                      <summary>查看前后内容</summary>
                      <CodeBlock>
                        {JSON.stringify({ before: change.before, after: change.after }, null, 2)}
                      </CodeBlock>
                    </details>
                  ),
                },
              ]}
              rowKey={(change) => `${change.category}:${change.path}`}
            />
          ) : (
            <p>定义与上次发布一致。</p>
          )}
          <details>
            <summary>完整归一化定义与校验摘要</summary>
            <CodeBlock>{JSON.stringify(validation, null, 2)}</CodeBlock>
          </details>
          {canWrite && (
            <Checkbox
              checked={acknowledged}
              disabled={!!busy || !!retryPending}
              onChange={(event) => onAcknowledge(event.target.checked)}
            >
              已核对语义差异与固定组件版本
            </Checkbox>
          )}
          {retryPending && !busy && (
            <Alert tone="warning" title="发布响应尚未确认">
              重试使用相同的校验计数、摘要与请求标识。
            </Alert>
          )}
        </div>
      )}
    </Modal>
  );
}
