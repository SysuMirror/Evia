import { useId } from 'react';
import type { DashboardVersion } from '@evia/api-client';
import {
  Badge,
  CodeBlock,
  EmptyPanel,
  LoadingPanel,
  PaginationBar,
  Panel,
  Select,
  Toolbar,
} from '@evia/design-system';
import type { CatalogPageProps } from '../catalog';
import { formatTime } from '../analysis-model';
import { CatalogError } from '../catalog-helpers';

import { DashboardRunView, type SavedRunWork } from './run';
type DashboardHistoryProps = CatalogPageProps & {
  dashboardID: string;
  publishedVersion: number;
  versionNumber?: number;
  displayVersion?: DashboardVersion;
  options: DashboardVersion[];
  busy: string;
  runWork: SavedRunWork;
  page: number;
  count: number;
  hasNext: boolean;
  versionsFetching: boolean;
  versionsError: Error | null;
  selectedPending: boolean;
  selectedFetching: boolean;
  selectedError: Error | null;
  onSelectVersion: (version: number) => void;
  onRetryVersion: () => void;
  onPrevious: () => void;
  onNext: () => void;
  onRunWork: (work: SavedRunWork) => void;
  onAccessDenied: (error: Error) => void;
};
export function DashboardHistory({
  project,
  environment,
  session,
  dashboardID,
  publishedVersion,
  versionNumber,
  displayVersion,
  options,
  busy,
  runWork,
  page,
  count,
  hasNext,
  versionsFetching,
  versionsError,
  selectedPending,
  selectedFetching,
  selectedError,
  onSelectVersion,
  onRetryVersion,
  onPrevious,
  onNext,
  onRunWork,
  onAccessDenied,
}: DashboardHistoryProps) {
  const id = useId();
  return (
    <div className="section-stack dashboard-published">
      {publishedVersion === 0 ? (
        <EmptyPanel description="尚无发布版本。保存草稿并校验后，可发布第一个不可变版本。" />
      ) : (
        <>
          <Panel label="选择已发布版本" className="dashboard-version-picker">
            <Toolbar>
              <label htmlFor={`${id}-version`}>已发布版本</label>
              <Select
                id={`${id}-version`}
                value={versionNumber ? String(versionNumber) : ''}
                disabled={
                  !!busy ||
                  runWork.busy ||
                  runWork.dirty ||
                  !!runWork.retryPending ||
                  versionsFetching
                }
                onChange={(event) => onSelectVersion(Number(event.target.value))}
                options={[
                  { value: '', label: '选择版本' },
                  ...options.map((item) => ({
                    value: String(item.version),
                    label: `v${item.version}，${item.definition.title}${item.version === publishedVersion ? '（最新发布）' : ''}`,
                  })),
                ]}
              />
              {displayVersion?.dashboard_id === dashboardID && (
                <div className="dashboard-version-metadata">
                  <Badge tone="success">不可变版本 v{displayVersion.version}</Badge>
                  <span>{displayVersion.definition.widgets.length} 个组件</span>
                  <details>
                    <summary>版本来源</summary>
                    <dl className="detail-list">
                      <div>
                        <dt>草稿修订</dt>
                        <dd>r{displayVersion.draft_revision}</dd>
                      </div>
                      <div>
                        <dt>发布时间</dt>
                        <dd>
                          {formatTime(displayVersion.created_at, project.timezone)}，
                          {project.timezone}
                        </dd>
                      </div>
                    </dl>
                  </details>
                </div>
              )}
            </Toolbar>
            <CatalogError error={versionsError} />
            {(page > 1 || hasNext) && (
              <PaginationBar
                page={page}
                count={count}
                hasNext={hasNext}
                busy={versionsFetching || !!busy || runWork.busy}
                onPrevious={onPrevious}
                onNext={onNext}
              />
            )}
          </Panel>
          {selectedPending && <LoadingPanel label="正在读取不可变版本" />}
          {!!selectedError && <CatalogError error={selectedError} retry={onRetryVersion} />}
          {displayVersion?.dashboard_id === dashboardID && (
            <>
              <DashboardRunView
                key={displayVersion.dashboard_id}
                disabled={selectedFetching || versionNumber !== displayVersion.version}
                version={displayVersion}
                context={{ project, environment, session }}
                onWorkChange={onRunWork}
                onDenied={onAccessDenied}
              />
              <details>
                <summary>查看该版本的固定配置与依赖</summary>
                <CodeBlock>
                  {JSON.stringify(
                    {
                      definition: displayVersion.definition,
                      dependencies: displayVersion.dependencies,
                    },
                    null,
                    2,
                  )}
                </CodeBlock>
              </details>
            </>
          )}
        </>
      )}
    </div>
  );
}
