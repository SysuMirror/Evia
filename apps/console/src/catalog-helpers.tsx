import { ApiError, type DraftInput } from '@evia/api-client';
import { CodeBlock, ErrorPanel } from '@evia/design-system';

export type JsonObject = Record<string, unknown>;
export const emptySchema: JsonObject = {
  type: 'object',
  properties: {},
  required: [],
  additionalProperties: false,
};
export const pretty = (value: unknown) => JSON.stringify(value ?? {}, null, 2);
export function objectValue(value: unknown): JsonObject {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as JsonObject)
    : {};
}
export function parseObject(text: string, label: string): JsonObject {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new Error(`${label}不是有效的 JSON`);
  }
  if (value === null || typeof value !== 'object' || Array.isArray(value))
    throw new Error(`${label}必须为 JSON 对象`);
  return value as JsonObject;
}
// The server validates the supported subset inside the open JSON object.
export const wireObject = (value: JsonObject): DraftInput['schema'] => value;
export const environmentLabel = {
  development: '开发环境',
  staging: '预发布环境',
  production: '生产环境',
};
export function CatalogError({ error, retry }: { error: Error | null; retry?: () => void }) {
  if (!error) return null;
  const details = error instanceof ApiError ? error.details : undefined;
  return (
    <div className="inline-notice">
      <ErrorPanel
        message={error.message}
        requestID={error instanceof ApiError ? error.requestID : undefined}
        retry={retry}
      />
      {details && <CodeBlock label="错误字段路径">{pretty(details)}</CodeBlock>}
    </div>
  );
}

export function isCatalogAccessError(error: unknown): boolean {
  return error instanceof ApiError && [401, 403, 404].includes(error.status);
}
