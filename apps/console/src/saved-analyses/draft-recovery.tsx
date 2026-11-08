import type { SavedAnalysisDraft } from '@evia/api-client';
import { Alert, Button, CodeBlock, Panel } from '@evia/design-system';
import { revisionConflict } from './types';
type DraftRecoveryProps = {
  latest?: SavedAnalysisDraft;
  error: Error | null;
  busy: boolean;
  frozen: boolean;
  creationPending: boolean;
  publicationPending: boolean;
  reviewOpen: boolean;
  onSave: () => void;
  onReadLatest: () => void;
  onResumePublication: () => void;
  onRebaseLocal: () => void;
  onAdoptLatest: () => void;
};
export function SavedAnalysisDraftRecovery({
  latest,
  error,
  busy,
  frozen,
  creationPending,
  publicationPending,
  reviewOpen,
  onSave,
  onReadLatest,
  onResumePublication,
  onRebaseLocal,
  onAdoptLatest,
}: DraftRecoveryProps) {
  return (
    <>
      {publicationPending && !reviewOpen && (
        <Alert
          tone="warning"
          title="发布响应尚未确认"
          action={<Button onClick={() => onResumePublication()}>继续确认发布</Button>}
        >
          已保留相同的校验计数、摘要与请求标识。
        </Alert>
      )}
      {creationPending && !busy && (
        <Alert
          tone="warning"
          title="创建响应尚未确认"
          action={<Button onClick={onSave}>重试创建分析</Button>}
        >
          已保留原始定义与请求标识，重试不会重复创建。
        </Alert>
      )}
      {error && revisionConflict(error) && !latest && (
        <Alert
          tone="warning"
          title="远端修订已变化"
          action={
            <Button onClick={onReadLatest} disabled={!!busy}>
              读取最新草稿
            </Button>
          }
        >
          本地修改已保留。读取最新草稿后，选择继续保存本地内容或采用远端内容。
        </Alert>
      )}
      {latest && (
        <Panel title={`最新草稿 r${latest.draft_revision}`}>
          <div className="section-stack">
            <p>
              本地内容已保留，远端最新发布为{' '}
              {latest.published_version ? `v${latest.published_version}` : '尚未发布'}。
            </p>
            <details>
              <summary>查看最新草稿内容</summary>
              <CodeBlock>{JSON.stringify(latest.definition, null, 2)}</CodeBlock>
            </details>
            <div className="inline-actions">
              <Button disabled={frozen} onClick={onRebaseLocal}>
                以最新修订继续编辑本地内容
              </Button>
              <Button disabled={frozen} onClick={onAdoptLatest}>
                采用最新草稿
              </Button>
            </div>
          </div>
        </Panel>
      )}
    </>
  );
}
