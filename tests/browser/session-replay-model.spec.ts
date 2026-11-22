import { expect, test } from '@playwright/test';
import type { ContentItem } from '../../packages/api-client/src/index';
import {
  buildReplayTimeline,
  decodeReplayPayloads,
  latestSnapshot,
} from '../../apps/console/src/session-replay/model';

function encoded(value: unknown) {
  return Buffer.from(JSON.stringify(value)).toString('base64url');
}
function item(
  content: string,
  itemID: string,
  contentType = 'session_replay_snapshot',
): ContentItem {
  return {
    content_type: contentType,
    stream_id: 'stream-1',
    item_id: itemID,
    actor_type: 'browser',
    content,
    truncated: false,
    content_length: Buffer.byteLength(content),
    key_id: 'test-key',
    received_at: '2026-11-22T02:00:00Z',
  };
}

const rawPayload = {
  format: 'evia.session-replay.v1',
  route: '/login',
  created_at: '2026-11-22T02:00:00Z',
  payload: { html: '<html><body>masked</body></html>', viewport: { width: 1440, height: 900 } },
};

test('replay model reconstructs complete segmented payloads and selects latest snapshot', async () => {
  const data = encoded(rawPayload);
  const midpoint = Math.ceil(data.length / 2);
  const segments = [data.slice(0, midpoint), data.slice(midpoint)].map((part, index) =>
    item(
      JSON.stringify({
        format: 'evia.session-replay.v1',
        encoding: 'base64url',
        group_id: 'group-1',
        segment_index: index,
        segment_count: 2,
        data: part,
      }),
      `item-${index}`,
    ),
  );
  const payloads = await decodeReplayPayloads(segments.reverse());
  expect(payloads).toHaveLength(1);
  const snapshot = latestSnapshot(payloads);
  expect(snapshot?.route).toBe('/login');
  expect(snapshot?.viewport).toEqual({ width: 1440, height: 900 });
  expect(snapshot?.html).toContain('masked');
});

function v2Item(payload: unknown, groupID: string, contentType: string): ContentItem {
  return item(
    JSON.stringify({
      format: 'evia.session-replay.v1',
      encoding: 'base64url',
      group_id: groupID,
      segment_index: 0,
      segment_count: 1,
      data: encoded(payload),
    }),
    `${groupID}-item`,
    contentType,
  );
}

test('replay v2 builds a contiguous exact timeline and detects gaps', async () => {
  const base = {
    format: 'evia.session-replay.v1',
    protocol_version: 2,
    document_id: '11111111-1111-4111-8111-111111111111',
    document_seq: 1,
    time_origin_ms: 1,
    route: '/home',
    created_at: '2026-11-22T02:00:00Z',
    capture_scope: 'sanitized-light-dom-v1',
    mask_version: 1,
  };
  const snapshot = v2Item(
    {
      ...base,
      payload_id: 'snapshot',
      payload_seq: 1,
      payload: {
        kind: 'snapshot',
        base_seq: 2,
        html: '<html data-evia-rid="1"><body data-evia-rid="2"><!--evia-rid:3-->old</body></html>',
        viewport: { width: 1440, height: 900 },
      },
    },
    'snapshot',
    'session_replay_snapshot',
  );
  const mutations = v2Item(
    {
      ...base,
      payload_id: 'mutations',
      payload_seq: 2,
      payload: {
        kind: 'mutations',
        records: [
          { seq: 3, at: 10, type: 'text', data: { node_id: 3, text: 'new' } },
          {
            seq: 4,
            at: 20,
            type: 'attribute',
            data: { node_id: 2, namespace: null, name: 'hidden', value: '' },
          },
        ],
      },
    },
    'mutations',
    'session_replay_mutations',
  );
  const payloads = await decodeReplayPayloads([mutations, snapshot]);
  const timeline = buildReplayTimeline(payloads);
  expect(timeline?.exact).toBe(true);
  expect(timeline?.records.map((record) => record.seq)).toEqual([3, 4]);

  const gapPayload = structuredClone(
    JSON.parse(
      Buffer.from((JSON.parse(mutations.content) as { data: string }).data, 'base64url').toString(),
    ),
  );
  gapPayload.payload.records[1].seq = 5;
  const gap = buildReplayTimeline(
    await decodeReplayPayloads([snapshot, v2Item(gapPayload, 'gap', 'session_replay_mutations')]),
  );
  expect(gap?.exact).toBe(false);
  expect(gap?.issue).toContain('#4');
});

test('replay model skips incomplete groups and rejects conflicting segments', async () => {
  const data = encoded(rawPayload);
  const first = item(
    JSON.stringify({
      format: 'evia.session-replay.v1',
      encoding: 'base64url',
      group_id: 'group-1',
      segment_index: 0,
      segment_count: 2,
      data,
    }),
    'item-1',
  );
  expect(await decodeReplayPayloads([first])).toEqual([]);
  await expect(decodeReplayPayloads([first, { ...first, item_id: 'item-2' }])).rejects.toThrow(
    '回放分段冲突',
  );
});
