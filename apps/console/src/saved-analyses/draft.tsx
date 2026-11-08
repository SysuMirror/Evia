import { useId, type ReactNode } from 'react';
import type { Environment, SavedAnalysisDefinition } from '@evia/api-client';
import { Badge, Button, CodeBlock, CopyButton, Toolbar } from '@evia/design-system';
import { SavedAnalysisEditor } from '../saved-analysis-editor';
import type { SavedAnalysisWorkspace } from './types';
type SavedAnalysisDraftProps = {
  workspace: SavedAnalysisWorkspace;
  projectID: string;
  actorID: string;
  environment: Environment;
  canWrite: boolean;
  busy: string;
  frozen: boolean;
  dirty: boolean;
  editorValid: boolean;
  editorPending: boolean;
  editorRevision: number | '';
  publicationPending: boolean;
  onDefinitionChange: (definition: SavedAnalysisDefinition) => void;
  onValidityChange: (valid: boolean) => void;
  onPendingChange: (pending: boolean) => void;
  onSave: () => void;
  onValidate: () => void;
  onReadLatest: () => void;
  recovery: ReactNode;
};
export function SavedAnalysisDraftView({
  workspace,
  projectID,
  actorID,
  environment,
  canWrite,
  busy,
  frozen,
  dirty,
  editorValid,
  editorPending,
  editorRevision,
  publicationPending,
  onDefinitionChange,
  onValidityChange,
  onPendingChange,
  onSave,
  onValidate,
  onReadLatest,
  recovery,
}: SavedAnalysisDraftProps) {
  const id = useId();
  return (
    <div className="section-stack saved-analysis-detail">
      {workspace.draft && (
        <Toolbar
          actions={
            <Button onClick={onReadLatest} disabled={!!busy || publicationPending}>
              读取最新草稿
            </Button>
          }
        >
          <div className="inline-actions">
            <Badge tone="neutral">草稿 r{workspace.draft.draft_revision}</Badge>
            <Badge tone={workspace.draft.published_version ? 'success' : 'neutral'}>
              {workspace.draft.published_version
                ? `最新发布 v${workspace.draft.published_version}`
                : '尚未发布'}
            </Badge>
            {dirty && <Badge tone="warning">未保存</Badge>}
          </div>
        </Toolbar>
      )}
      <SavedAnalysisEditor
        key={`${workspace.draft?.analysis_id ?? 'new'}:${editorRevision}`}
        value={workspace.definition}
        onChange={onDefinitionChange}
        projectID={projectID}
        actorID={actorID}
        environment={environment}
        readOnly={!canWrite}
        disabled={frozen}
        onValidityChange={onValidityChange}
        onPendingChange={onPendingChange}
      />
      <form
        id={`${id}-draft`}
        className="section-stack"
        onSubmit={(event) => {
          event.preventDefault();
          onSave();
        }}
      >
        <Toolbar
          actions={
            <>
              {canWrite && (
                <Button
                  type="submit"
                  variant="primary"
                  loading={busy === '创建分析' || busy === '保存草稿'}
                  disabled={
                    frozen || !editorValid || editorPending || (!!workspace.draft && !dirty)
                  }
                >
                  {workspace.draft ? '保存草稿' : '创建分析'}
                </Button>
              )}
              {workspace.draft && (
                <Button
                  onClick={onValidate}
                  loading={busy === '校验已保存草稿'}
                  disabled={frozen || dirty || !editorValid || editorPending}
                >
                  校验已保存草稿
                </Button>
              )}
            </>
          }
        >
          <span className="field-help">
            {dirty
              ? '先保存修改，再校验与发布。'
              : '校验仅检查已保存的配置与固定依赖，不执行查询。'}
          </span>
        </Toolbar>
      </form>
      {recovery}
      {workspace.draft && (
        <details>
          <summary>资源标识与定义</summary>
          <CopyButton value={workspace.draft.analysis_id} label="复制分析标识" />
          <CodeBlock>{JSON.stringify(workspace.draft.definition, null, 2)}</CodeBlock>
        </details>
      )}
    </div>
  );
}
