import type { ClientOptions, Properties, PublicConfig } from './types.js';

export class EviaSDKError extends Error {
  constructor(public readonly code: string) {
    super(code);
    this.name = 'EviaSDKError';
  }
}

export const uuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
export const customEventPattern = /^[a-z][a-z0-9_]{0,63}$/;
const propertyPattern = /^[a-z][a-z0-9_]{0,63}$/;
const encoder = new TextEncoder();
export const byteLength = (text: string): number => encoder.encode(text).byteLength;
export const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

export function requireText(
  value: unknown,
  max: number,
  nonempty = false,
): asserts value is string {
  if (
    typeof value !== 'string' ||
    (nonempty && !value) ||
    value.length > max * 2 ||
    [...value].length > max ||
    /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value)
  ) {
    throw new EviaSDKError('INVALID_TEXT');
  }
}

export function copyProperties(input: Properties): Properties {
  if (
    !isRecord(input) ||
    (Object.getPrototypeOf(input) !== Object.prototype && Object.getPrototypeOf(input) !== null)
  )
    throw new EviaSDKError('INVALID_PROPERTIES');
  const keys = Reflect.ownKeys(input);
  if (keys.length > 50) throw new EviaSDKError('INVALID_PROPERTIES');
  const output: Record<string, string | number | boolean | readonly (string | number | boolean)[]> =
    Object.create(null);
  const scalar = (value: unknown): string | number | boolean => {
    if (typeof value === 'string') {
      requireText(value, 1024);
      return value;
    }
    if (typeof value === 'boolean') return value;
    if (
      typeof value === 'number' &&
      Number.isFinite(value) &&
      Math.abs(value) <= Number.MAX_SAFE_INTEGER
    )
      return value;
    throw new EviaSDKError('INVALID_PROPERTY_VALUE');
  };
  for (const key of keys) {
    if (typeof key !== 'string' || !propertyPattern.test(key))
      throw new EviaSDKError('INVALID_PROPERTY_KEY');
    const descriptor = Object.getOwnPropertyDescriptor(input, key);
    if (!descriptor || !descriptor.enumerable || !('value' in descriptor))
      throw new EviaSDKError('INVALID_PROPERTIES');
    const value: unknown = descriptor.value;
    if (Array.isArray(value)) {
      if (value.length > 20 || Object.keys(value).length !== value.length)
        throw new EviaSDKError('INVALID_PROPERTY_VALUE');
      output[key] = Object.freeze(
        Array.from({ length: value.length }, (_, index) => {
          const item = Object.getOwnPropertyDescriptor(value, String(index));
          if (!item || !('value' in item)) throw new EviaSDKError('INVALID_PROPERTY_VALUE');
          return scalar(item.value);
        }),
      );
    } else output[key] = scalar(value);
  }
  return Object.freeze(output);
}

export function boundedInteger(
  value: number | undefined,
  fallback: number,
  min: number,
  max: number,
): number {
  const result = value ?? fallback;
  if (!Number.isSafeInteger(result) || result < min || result > max)
    throw new EviaSDKError('INVALID_OPTION');
  return result;
}

export function validateOptions(options: ClientOptions): string {
  requireText(options.endpoint, 2048, true);
  let url: URL;
  try {
    url = new URL(options.endpoint);
  } catch {
    throw new EviaSDKError('INVALID_ENDPOINT');
  }
  if (!/^https?:$/.test(url.protocol) || url.username || url.password || url.search || url.hash)
    throw new EviaSDKError('INVALID_ENDPOINT');
  requireText(options.writeKey, 128, true);
  if (byteLength(options.writeKey) > 128) throw new EviaSDKError('INVALID_OPTION');
  if (
    !uuidPattern.test(options.projectId) ||
    !['development', 'staging', 'production'].includes(options.environment)
  )
    throw new EviaSDKError('INVALID_SCOPE');
  return url.href.replace(/\/$/, '');
}

export function validateConfig(
  value: unknown,
  projectId: string,
  environment: string,
): PublicConfig {
  if (
    !isRecord(value) ||
    value.format !== 'evia.public-config.v1' ||
    value.project_id !== projectId ||
    value.environment !== environment ||
    !Number.isSafeInteger(value.release_revision) ||
    Number(value.release_revision) < 1 ||
    !Array.isArray(value.events) ||
    value.events.length > 200
  )
    throw new EviaSDKError('INVALID_CONFIG');
  const seen = new Set<string>();
  for (const event of value.events) {
    if (
      !isRecord(event) ||
      typeof event.event_key !== 'string' ||
      !/^(sys\.)?[a-z][a-z0-9_]{0,63}$/.test(event.event_key) ||
      seen.has(event.event_key) ||
      !Number.isSafeInteger(event.schema_version) ||
      Number(event.schema_version) < 1 ||
      !isRecord(event.schema)
    )
      throw new EviaSDKError('INVALID_CONFIG');
    seen.add(event.event_key);
  }
  return value as unknown as PublicConfig;
}
