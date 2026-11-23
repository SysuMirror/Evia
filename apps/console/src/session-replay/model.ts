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
  protocolVersion: number;
  documentID?: string;
  documentSeq?: number;
  payloadSeq?: number;
  payload: Record<string, unknown>;
}

export interface ReplaySnapshot extends ReplayPayload {
  html: string;
  viewport: { width: number; height: number };
  baseSeq: number;
}

export interface ReplayRecordV2 {
  seq: number;
  at: number;
  type: string;
  data: Record<string, unknown>;
}

export interface ReplayTimeline {
  snapshot: ReplaySnapshot;
  records: ReplayRecordV2[];
  exact: boolean;
  issue?: string;
}

export interface ReplayChapter {
  id: string;
  index: number;
  documentID?: string;
  documentSeq?: number;
  startedAt: string;
  endedAt?: string;
  timeline: ReplayTimeline;
  resyncedFromIssue?: string;
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
      protocol_version?: number;
      document_id?: string;
      document_seq?: number;
      payload_seq?: number;
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
      protocolVersion: value.protocol_version === 2 ? 2 : 1,
      documentID: typeof value.document_id === 'string' ? value.document_id : undefined,
      documentSeq: Number.isSafeInteger(value.document_seq) ? value.document_seq : undefined,
      payloadSeq: Number.isSafeInteger(value.payload_seq) ? value.payload_seq : undefined,
      payload: value.payload as Record<string, unknown>,
    });
  }
  return payloads.sort((left, right) => left.createdAt.localeCompare(right.createdAt));
}

function replaySnapshots(payloads: ReplayPayload[]): ReplaySnapshot[] {
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
      baseSeq: Number.isSafeInteger(payload.payload.base_seq)
        ? Number(payload.payload.base_seq)
        : 0,
    });
  }
  return snapshots.sort((left, right) => {
    const documentOrder = (left.documentSeq ?? 0) - (right.documentSeq ?? 0);
    if (documentOrder !== 0) return documentOrder;
    const payloadOrder = (left.payloadSeq ?? 0) - (right.payloadSeq ?? 0);
    return payloadOrder !== 0 ? payloadOrder : left.createdAt.localeCompare(right.createdAt);
  });
}

export function latestSnapshot(payloads: ReplayPayload[]): ReplaySnapshot | undefined {
  return replaySnapshots(payloads).at(-1);
}

function timelineFromSnapshot(
  payloads: ReplayPayload[],
  snapshot: ReplaySnapshot,
  nextSnapshot?: ReplaySnapshot,
): ReplayTimeline {
  if (snapshot.protocolVersion !== 2 || !snapshot.documentID)
    return { snapshot, records: [], exact: false, issue: 'v1 录制仅支持静态快照。' };
  const upperSeq =
    nextSnapshot?.documentID === snapshot.documentID
      ? nextSnapshot.baseSeq
      : Number.MAX_SAFE_INTEGER;
  const records: ReplayRecordV2[] = [];
  for (const payload of payloads) {
    if (
      payload.protocolVersion !== 2 ||
      payload.documentID !== snapshot.documentID ||
      payload.contentType !== 'session_replay_mutations' ||
      !Array.isArray(payload.payload.records)
    )
      continue;
    for (const value of payload.payload.records) {
      if (!value || typeof value !== 'object' || Array.isArray(value)) continue;
      const record = value as Partial<ReplayRecordV2>;
      if (
        Number.isSafeInteger(record.seq) &&
        Number.isFinite(record.at) &&
        typeof record.type === 'string' &&
        record.data &&
        typeof record.data === 'object' &&
        !Array.isArray(record.data) &&
        Number(record.seq) > snapshot.baseSeq &&
        Number(record.seq) <= upperSeq
      )
        records.push(record as ReplayRecordV2);
    }
  }
  records.sort((left, right) => left.seq - right.seq);
  let expected = snapshot.baseSeq + 1;
  for (const record of records) {
    if (record.seq !== expected)
      return {
        snapshot,
        records: records.filter((item) => item.seq < expected),
        exact: false,
        issue: `记录序号在 #${expected} 处中断。`,
      };
    expected++;
  }
  return { snapshot, records, exact: true };
}

export function buildReplayChapters(payloads: ReplayPayload[]): ReplayChapter[] {
  const snapshots = replaySnapshots(payloads);
  const chapters: ReplayChapter[] = [];
  for (let index = 0; index < snapshots.length; index++) {
    const snapshot = snapshots[index]!;
    const nextSnapshot = snapshots[index + 1];
    const timeline = timelineFromSnapshot(payloads, snapshot, nextSnapshot);
    const previous = chapters.at(-1);
    const resyncedFromIssue =
      previous && previous.documentID === snapshot.documentID && !previous.timeline.exact
        ? (previous.timeline.issue ?? '前一章节不完整。')
        : undefined;
    chapters.push({
      id: snapshot.groupID,
      index,
      documentID: snapshot.documentID,
      documentSeq: snapshot.documentSeq,
      startedAt: snapshot.createdAt,
      endedAt: nextSnapshot?.createdAt,
      timeline,
      ...(resyncedFromIssue ? { resyncedFromIssue } : {}),
    });
  }
  return chapters;
}

export function buildReplayTimeline(payloads: ReplayPayload[]): ReplayTimeline | undefined {
  return buildReplayChapters(payloads).at(-1)?.timeline;
}

function replayNodeMap(
  root: Document | DocumentFragment,
  existing = new Map<number, Node>(),
): Map<number, Node> {
  for (const element of Array.from(root.querySelectorAll('[data-evia-rid]'))) {
    const id = Number(element.getAttribute('data-evia-rid'));
    if (!Number.isSafeInteger(id) || id < 1) throw new Error('回放节点标识无效。');
    const prior = existing.get(id);
    if (prior?.isConnected) throw new Error('回放节点标识重复。');
    existing.set(id, element);
  }
  const owner = root instanceof Document ? root : root.ownerDocument;
  if (!owner) throw new Error('回放节点文档无效。');
  const walker = owner.createTreeWalker(root, NodeFilter.SHOW_COMMENT);
  const markers: Comment[] = [];
  while (walker.nextNode()) {
    const comment = walker.currentNode as Comment;
    if (/^evia-rid:[1-9][0-9]*$/.test(comment.data)) markers.push(comment);
  }
  for (const marker of markers) {
    const id = Number(marker.data.slice('evia-rid:'.length));
    const target = marker.nextSibling;
    if (!target || !Number.isSafeInteger(id)) throw new Error('回放文本节点标识无效。');
    const prior = existing.get(id);
    if (prior?.isConnected) throw new Error('回放节点标识重复。');
    existing.set(id, target);
    marker.remove();
  }
  return existing;
}

function sanitizeReplayRoot(root: Document | DocumentFragment): void {
  root
    .querySelectorAll('script,noscript,iframe,object,embed,base,link,meta[http-equiv]')
    .forEach((node) => node.remove());
  for (const element of Array.from(root.querySelectorAll('*'))) {
    for (const attribute of Array.from(element.attributes)) {
      const name = attribute.name.toLowerCase();
      if (
        name.startsWith('on') ||
        [
          'src',
          'srcset',
          'href',
          'poster',
          'action',
          'formaction',
          'ping',
          'srcdoc',
          'nonce',
        ].includes(name) ||
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
}

export function applyReplayRecords(document: Document, records: ReplayRecordV2[]): void {
  const nodes = replayNodeMap(document);
  const connected = (id: unknown): Node => {
    if (!Number.isSafeInteger(id)) throw new Error('回放记录缺少节点标识。');
    const node = nodes.get(Number(id));
    if (!node?.isConnected) throw new Error(`回放节点 #${id} 不存在。`);
    return node;
  };
  for (const record of records) {
    const data = record.data;
    if (record.type === 'text') {
      const node = connected(data.node_id);
      if (node.nodeType !== Node.TEXT_NODE && node.nodeType !== Node.COMMENT_NODE)
        throw new Error('回放文本目标类型无效。');
      if (typeof data.text !== 'string') throw new Error('回放文本值无效。');
      node.textContent = data.text;
    } else if (record.type === 'attribute') {
      const node = connected(data.node_id);
      if (!(node instanceof Element) || typeof data.name !== 'string')
        throw new Error('回放属性目标无效。');
      if (!['class', 'style', 'hidden'].includes(data.name))
        throw new Error('回放属性不在安全白名单。');
      const namespace = typeof data.namespace === 'string' ? data.namespace : null;
      if (data.value === null) node.removeAttributeNS(namespace, data.name);
      else if (typeof data.value === 'string') {
        const value =
          data.name === 'style'
            ? data.value.replace(/url\([^)]*\)/gi, 'url()').slice(0, 2000)
            : data.value;
        node.setAttributeNS(namespace, data.name, value);
      } else throw new Error('回放属性值无效。');
    } else if (record.type === 'children') {
      const parent = connected(data.parent_id);
      const removed = Array.isArray(data.removed_ids) ? data.removed_ids : [];
      for (const id of removed) {
        const node = connected(id);
        if (node.parentNode !== parent) throw new Error('回放删除目标不属于指定父节点。');
        node.parentNode.removeChild(node);
        nodes.delete(Number(id));
      }
      if (data.added !== null && data.added !== undefined) {
        if (!data.added || typeof data.added !== 'object' || Array.isArray(data.added))
          throw new Error('回放新增片段无效。');
        const added = data.added as { root_ids?: unknown; html?: unknown };
        if (!Array.isArray(added.root_ids) || typeof added.html !== 'string')
          throw new Error('回放新增片段无效。');
        const range = document.createRange();
        range.selectNodeContents(parent);
        const fragment = range.createContextualFragment(added.html);
        sanitizeReplayRoot(fragment);
        replayNodeMap(fragment, nodes);
        const roots = added.root_ids.map((id) => {
          if (!Number.isSafeInteger(id)) throw new Error('回放新增根标识无效。');
          const node = nodes.get(Number(id));
          if (!node) throw new Error('回放新增根不存在。');
          return node;
        });
        const before = data.before_id === null ? null : connected(data.before_id);
        if (before && before.parentNode !== parent) throw new Error('回放插入锚点无效。');
        for (const root of roots) parent.insertBefore(root, before);
      }
    }
  }
}

export function sandboxSnapshotHTML(html: string): string {
  const document = new DOMParser().parseFromString(html, 'text/html');
  sanitizeReplayRoot(document);
  const policy = document.createElement('meta');
  policy.httpEquiv = 'Content-Security-Policy';
  policy.content =
    "default-src 'none'; connect-src 'none'; script-src 'none'; object-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'; style-src 'unsafe-inline'; img-src data:";
  document.head.prepend(policy);
  return `<!doctype html>${document.documentElement.outerHTML}`;
}

export function shortReplayID(value: string): string {
  return value.length <= 18 ? value : `${value.slice(0, 8)}…${value.slice(-6)}`;
}

export function replayInstant(value: string): string {
  return new Date(value).toLocaleString('zh-CN', { hour12: false });
}
