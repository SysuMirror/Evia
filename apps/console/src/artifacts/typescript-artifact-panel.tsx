import { useQuery } from '@tanstack/react-query';
import { api, ApiError, type Project, type TypeScriptArtifact } from '@evia/api-client';
import { Alert, Button, CodeBlock, CopyButton, Panel } from '@evia/design-system';
import { downloadTypeScript, formatByteSize } from './model';

type Props = { project: Project; releaseID: string };

export function TypeScriptArtifactPanel({ project, releaseID }: Props) {
  const artifact = useQuery({
    queryKey: ['typescript-artifact', project.id, releaseID],
    queryFn: ({ signal }) => api.getReleaseTypeScriptArtifact(project.id, releaseID, signal),
    enabled: !!releaseID,
  });

  if (artifact.isError) {
    return (
      <Panel title="TypeScript 类型产物" className="resource-section">
        <Alert
          tone="error"
          title="无法生成类型产物"
          action={<Button onClick={() => void artifact.refetch()}>重试</Button>}
        >
          {artifact.error instanceof ApiError ? artifact.error.message : '请稍后再试。'}
        </Alert>
      </Panel>
    );
  }

  if (artifact.isPending || !artifact.data) {
    return (
      <Panel title="TypeScript 类型产物" className="resource-section">
        <p className="evia-field-hint">正在生成类型产物。</p>
      </Panel>
    );
  }

  const data: TypeScriptArtifact = artifact.data;
  return (
    <Panel
      title="TypeScript 类型产物"
      className="resource-section"
      action={
        <Button variant="secondary" onClick={() => downloadTypeScript(data)}>
          下载 .ts
        </Button>
      }
    >
      <dl className="project-details">
        <div>
          <dt>语言版本</dt>
          <dd>
            {data.language} ≥ {data.language_version}
          </dd>
        </div>
        <div>
          <dt>事件类型数</dt>
          <dd>{data.event_count}</dd>
        </div>
        <div>
          <dt>产物大小</dt>
          <dd>{formatByteSize(data.byte_size)}</dd>
        </div>
        <div>
          <dt>校验和</dt>
          <dd>
            <code>{data.checksum}</code> <CopyButton value={data.checksum} />
          </dd>
        </div>
      </dl>
      <p className="evia-field-hint">
        接口名按事件标识确定性生成；产物只包含发布清单中的不可变 schema，未发布草稿不会出现。
      </p>
      <CodeBlock label="TypeScript 类型产物源码">{data.source}</CodeBlock>
    </Panel>
  );
}
