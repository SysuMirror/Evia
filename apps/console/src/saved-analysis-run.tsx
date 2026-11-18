import { useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import type { Environment, Project, SavedAnalysisVersion, Session } from '@evia/api-client';
import { Alert, Button, EmptyPanel, Panel } from '@evia/design-system';
import { SavedExportDialog } from './saved-analysis-run/export-dialog';
import { initialForm } from './analysis-model';
import { CatalogError, environmentLabel } from './catalog-helpers';
import { SavedResultPresentation } from './saved-analysis-run/result-presentation';
import { SavedTimeControls, savedExecutionBody } from './saved-analysis-run/time-controls';
import { activeSavedJob as active, type SavedRunWork } from './saved-analysis-run/types';
import { useSavedExecution } from './saved-analysis-run/use-execution';
export { savedAnalysisResultChart } from './saved-analysis-run/result-chart';
export type { SavedRunWork } from './saved-analysis-run/types';

export function SavedAnalysisRun({
  version,
  project,
  environment,
  session,
  onWorkChange,
  onAccessDenied,
  disabled = false,
}: {
  version: SavedAnalysisVersion;
  project: Project;
  environment: Environment;
  session: Session;
  onWorkChange: (work: SavedRunWork) => void;
  onAccessDenied: (error: Error) => void;
  disabled?: boolean;
}) {
  const [range, setRange] = useState(() => {
    const form = initialForm(project.timezone);
    return { from: form.from, to: form.to, timezone: project.timezone };
  });
  const [rangeBaseline, setRangeBaseline] = useState(() => JSON.stringify(range));
  const [rangeOpen, setRangeOpen] = useState(false);
  const [rangeError, setRangeError] = useState('');
  const [exportOpen, setExportOpen] = useState(false);
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const exportAllowed = project.permissions.includes('data.export');
  const execution = useSavedExecution({
    version,
    project,
    environment,
    session,
    disabled,
    rangeBody: () => savedExecutionBody(range, version.definition.query.kind),
    onInvalidRange: (message) => {
      setRangeError(message);
      setRangeOpen(true);
    },
    onExecuted: (body) =>
      setRangeBaseline(
        JSON.stringify({
          from: body.time_range.from.slice(0, 16),
          to: body.time_range.to.slice(0, 16),
          timezone: body.timezone,
        }),
      ),
    onAccessDenied,
  });
  const { attempt, snapshot, error, notice, busy, observing, denied, execute, cancel } = execution;
  useEffect(() => setRangeOpen(false), [execution.identity]);
  const workDirty = !!attempt || active(snapshot?.job) || JSON.stringify(range) !== rangeBaseline;
  useEffect(() => {
    onWorkChange({ dirty: workDirty, busy, retryPending: !!attempt });
  }, [onWorkChange, workDirty, busy, attempt]);
  useEffect(
    () => () => {
      onWorkChange({ dirty: false, busy: false });
    },
    [onWorkChange],
  );
  const changed =
    !!snapshot &&
    (snapshot.version.version !== version.version ||
      Date.parse(snapshot.body.time_range.from) !== Date.parse(`${range.from}Z`) ||
      Date.parse(snapshot.body.time_range.to) !== Date.parse(`${range.to}Z`) ||
      snapshot.body.timezone !== range.timezone);
  if (denied) return <CatalogError error={error} />;
  return (
    <div className="saved-analysis-run section-stack">
      <Panel label="执行已发布分析" className="saved-analysis-run-controls">
        <form
          className="saved-analysis-run-form"
          onSubmit={(event) => {
            event.preventDefault();
            void execute();
          }}
        >
          <div className="analysis-query-footer">
            <div className="saved-analysis-run-context">
              <strong>
                运行 v{version.version}，{environmentLabel[version.environment]}
              </strong>
              <div className="analysis-query-summary">
                <span>
                  {range.from.replace('T', ' ')} 至 {range.to.replace('T', ' ')}（不含），UTC
                </span>
                <span>
                  显示时区 {range.timezone}，最多 {version.definition.query.limit ?? 200} 行
                </span>
              </div>
            </div>
            <div className="inline-actions">
              <Button
                onClick={() => setRangeOpen(true)}
                disabled={disabled || busy || !!attempt}
                aria-haspopup="dialog"
              >
                执行时间范围
              </Button>
              {active(snapshot?.job) && (
                <Button onClick={() => void cancel()} loading={busy}>
                  取消查询
                </Button>
              )}
              {exportAllowed && (
                <Button
                  onClick={() => setExportOpen(true)}
                  disabled={disabled || busy || !!attempt}
                  aria-haspopup="dialog"
                >
                  导出此版本
                </Button>
              )}
              <Button
                type="submit"
                variant="primary"
                loading={busy}
                disabled={disabled || busy || active(snapshot?.job)}
              >
                {attempt ? '重试本次执行' : '运行已发布版本'}
              </Button>
            </div>
          </div>
        </form>
      </Panel>
      {attempt && !busy && (
        <Alert tone="warning" title="执行响应尚未确认">
          重试会复用 v{attempt.version.version}、原时间范围和同一请求标识，避免重复创建查询。
        </Alert>
      )}
      <CatalogError error={error} />
      {snapshot ? (
        <SavedResultPresentation
          snapshot={snapshot}
          changed={changed}
          observing={observing}
          notice={notice}
          onResume={execution.resume}
        />
      ) : (
        <EmptyPanel description="选择执行范围并运行，结果将固定显示本次发布版本、环境与时间。" />
      )}
      <SavedTimeControls
        range={range}
        setRange={setRange}
        rangeOpen={rangeOpen}
        rangeError={rangeError}
        setRangeError={setRangeError}
        kind={version.definition.query.kind}
        onClose={() => setRangeOpen(false)}
      />
      <SavedExportDialog
        open={exportOpen}
        onClose={() => setExportOpen(false)}
        version={version}
        range={range}
        project={project}
        session={session}
        onAccessDenied={onAccessDenied}
        onCreated={() => {
          void queryClient.invalidateQueries({ queryKey: ['exports', project.id] });
          void navigate(`/projects/${project.id}/exports?environment=${environment}`);
        }}
      />
    </div>
  );
}

export { SavedResultPresentation as SavedAnalysisResultPresentation } from './saved-analysis-run/result-presentation';
export type { ExecutionSnapshot as SavedAnalysisResultSnapshot } from './saved-analysis-run/types';
export {
  SavedTimeControls as SavedAnalysisTimeControls,
  savedExecutionBody as savedAnalysisExecutionBody,
} from './saved-analysis-run/time-controls';
