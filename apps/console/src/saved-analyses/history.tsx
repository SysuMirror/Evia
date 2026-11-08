import { useId } from 'react';
import type { SavedAnalysisVersion } from '@evia/api-client';
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
import { savedDefinitionSummary } from '../saved-analysis-model';
import { SavedAnalysisRun, type SavedRunWork } from '../saved-analysis-run';
type SavedAnalysisHistoryProps = CatalogPageProps & {
  analysisID: string;
  publishedVersion: number;
  versionNumber?: number;
  displayVersion?: SavedAnalysisVersion;
  options: SavedAnalysisVersion[];
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
export function SavedAnalysisHistory({
  project,
  environment,
  session,
  analysisID,
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
}: SavedAnalysisHistoryProps) {
  const id = useId();
  return (
    <div className="section-stack saved-analysis-published">
      {publishedVersion === 0 ? (
        <EmptyPanel description="尚无发布版本。保存草稿并校验后，可发布第一个不可变版本。" />
      ) : (
        <>
          <Panel label="选择已发布版本" className="saved-analysis-version-picker">
            <Toolbar>
              <label htmlFor={`${id}-version`}>已发布版本</label>
              <Select
                id={`${id}-version`}
                value={versionNumber ? String(versionNumber) : ''}
                disabled={!!busy || runWork.busy || !!runWork.retryPending || versionsFetching}
                onChange={(event) => onSelectVersion(Number(event.target.value))}
                options={[
                  { value: '', label: '选择版本' },
                  ...options.map((item) => ({
                    value: String(item.version),
                    label: `v${item.version}，${item.definition.title}${item.version === publishedVersion ? '（最新发布）' : ''}`,
                  })),
                ]}
              />
              {displayVersion?.analysis_id === analysisID && (
                <div className="saved-analysis-version-metadata">
                  <Badge tone="success">不可变版本 v{displayVersion.version}</Badge>
                  <span>{savedDefinitionSummary(displayVersion.definition)}</span>
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
          {displayVersion?.analysis_id === analysisID && (
            <>
              <SavedAnalysisRun
                key={displayVersion.analysis_id}
                disabled={selectedFetching || versionNumber !== displayVersion.version}
                version={displayVersion}
                project={project}
                environment={environment}
                session={session}
                onWorkChange={onRunWork}
                onAccessDenied={onAccessDenied}
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
