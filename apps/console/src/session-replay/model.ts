import type { ContentItem } from '@evia/api-client';

const FORMAT = 'evia.session-replay.v1';
const MAX_SEGMENTS = 1024;
const MAX_DECOMPRESSED_BYTES = 16 * 1024 * 1024;

interface SegmentEnvelope {
  format: string;
  encoding: 'gzip-base64url' | 'base64url';
  group_id: string;
  segment_index: number;
  segment_count: number;
  data: string;
}

export interface ReplayPayload {
  groupID: string;
  contentType: string;
  route: string;
  createdAt: string;
  payload: Record<string, unknown>;
}

export interface ReplaySnapshot extends ReplayPayload {
  html: string;
  viewport: { width: number; height: number };
}

function segmentEnvelope(content: string): SegmentEnvelope {
  const value = JSON.parse(content) as Partial<SegmentEnvelope>;
  if (
    value.format !== FORMAT ||
    (value.encoding !== 'gzip-base64url' && value.encoding !== 'base64url') ||
    typeof value.group_id !== 'string' ||
    value.group_id.length < 1 ||
    value.group_id.length > 128 ||
    !Number.isSafeInteger(value.segment_index) ||
    !Number.isSafeInteger(value.segment_count) ||
    value.segment_index! < 0 ||
    value.segment_count! < 1 ||
    value.segment_count! > MAX_SEGMENTS ||
    value.segment_index! >= value.segment_count! ||
    typeof value.data !== 'string' ||
    value.data.length > 20_000 ||
    !/^[A-Za-z0-9_-]*$/.test(value.data)
  )
    throw new Error('回放分段格式无效。');
  return value as SegmentEnvelope;
}

function base64URL(value: string): Uint8Array {
  const padded = value
    .replace(/-/g, '+')
    .replace(/_/g, '/')
    .padEnd(Math.ceil(value.length / 4) * 4, '=');
  const binary = atob(padded);
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
}

async function decodeGroup(
  encoding: SegmentEnvelope['encoding'],
  encoded: string,
): Promise<string> {
  const bytes = base64URL(encoded);
  if (bytes.length > MAX_DECOMPRESSED_BYTES) throw new Error('回放压缩数据超过读取上限。');
  if (encoding === 'base64url') return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  if (typeof DecompressionStream === 'undefined')
    throw new Error('当前浏览器不支持 gzip 回放解压。');
  const copy = new Uint8Array(bytes.byteLength);
  copy.set(bytes);
  const stream = new Blob([copy.buffer]).stream().pipeThrough(new DecompressionStream('gzip'));
  const output = new Uint8Array(await new Response(stream).arrayBuffer());
  if (output.length > MAX_DECOMPRESSED_BYTES) throw new Error('回放解压后超过 16 MiB 安全上限。');
  return new TextDecoder('utf-8', { fatal: true }).decode(output);
}

export async function decodeReplayPayloads(items: ContentItem[]): Promise<ReplayPayload[]> {
  const groups = new Map<
    string,
    {
      contentType: string;
      encoding: SegmentEnvelope['encoding'];
      count: number;
      segments: string[];
    }
  >();
  for (const item of items) {
    const segment = segmentEnvelope(item.content);
    const current = groups.get(segment.group_id) ?? {
      contentType: item.content_type,
      encoding: segment.encoding,
      count: segment.segment_count,
      segments: [],
    };
    if (
      current.contentType !== item.content_type ||
      current.encoding !== segment.encoding ||
      current.count !== segment.segment_count ||
      current.segments[segment.segment_index] !== undefined
    )
      throw new Error('回放分段冲突。');
    current.segments[segment.segment_index] = segment.data;
    groups.set(segment.group_id, current);
  }
  const payloads: ReplayPayload[] = [];
  for (const [groupID, group] of groups) {
    if (
      group.segments.length !== group.count ||
      group.segments.some((segment) => segment === undefined)
    )
      continue;
    const raw = await decodeGroup(group.encoding, group.segments.join(''));
    const value = JSON.parse(raw) as {
      format?: string;
      route?: string;
      created_at?: string;
      payload?: unknown;
    };
    if (
      value.format !== FORMAT ||
      typeof value.route !== 'string' ||
      value.route.length > 128 ||
      typeof value.created_at !== 'string' ||
      !Number.isFinite(Date.parse(value.created_at)) ||
      !value.payload ||
      typeof value.payload !== 'object' ||
      Array.isArray(value.payload)
    )
      throw new Error('回放载荷格式无效。');
    payloads.push({
      groupID,
      contentType: group.contentType,
      route: value.route,
      createdAt: value.created_at,
      payload: value.payload as Record<string, unknown>,
    });
  }
  return payloads.sort((left, right) => left.createdAt.localeCompare(right.createdAt));
}

export function latestSnapshot(payloads: ReplayPayload[]): ReplaySnapshot | undefined {
  const snapshots: ReplaySnapshot[] = [];
  for (const payload of payloads) {
    const html = payload.payload.html;
    const viewport = payload.payload.viewport as { width?: unknown; height?: unknown } | undefined;
    if (
      payload.contentType !== 'session_replay_snapshot' ||
      typeof html !== 'string' ||
      html.length > 4 * 1024 * 1024 ||
      !viewport ||
      !Number.isSafeInteger(viewport.width) ||
      !Number.isSafeInteger(viewport.height) ||
      Number(viewport.width) < 1 ||
      Number(viewport.width) > 16_384 ||
      Number(viewport.height) < 1 ||
      Number(viewport.height) > 16_384
    )
      continue;
    snapshots.push({
      ...payload,
      html,
      viewport: { width: Number(viewport.width), height: Number(viewport.height) },
    });
  }
  return snapshots.sort((left, right) => right.createdAt.localeCompare(left.createdAt))[0];
}

export function sandboxSnapshotHTML(html: string): string {
  const document = new DOMParser().parseFromString(html, 'text/html');
  document
    .querySelectorAll('script,noscript,iframe,object,embed,base,link,meta[http-equiv]')
    .forEach((node) => node.remove());
  for (const element of Array.from(document.querySelectorAll('*'))) {
    for (const attribute of Array.from(element.attributes)) {
      const name = attribute.name.toLowerCase();
      if (
        name.startsWith('on') ||
        ['src', 'href', 'action', 'formaction', 'srcdoc', 'nonce'].includes(name) ||
        name.includes('token') ||
        name.includes('authorization') ||
        name.includes('cookie')
      )
        element.removeAttribute(attribute.name);
      else if (name === 'style')
        element.setAttribute(
          'style',
          attribute.value.replace(/url\([^)]*\)/gi, 'url()').slice(0, 2000),
        );
    }
  }
  const policy = document.createElement('meta');
  policy.httpEquiv = 'Content-Security-Policy';
  policy.content = "default-src 'none'; style-src 'unsafe-inline'; img-src data:";
  document.head.prepend(policy);
  return `<!doctype html>${document.documentElement.outerHTML}`;
}

export function shortReplayID(value: string): string {
  return value.length <= 18 ? value : `${value.slice(0, 8)}…${value.slice(-6)}`;
}

export function replayInstant(value: string): string {
  return new Date(value).toLocaleString('zh-CN', { hour12: false });
}
