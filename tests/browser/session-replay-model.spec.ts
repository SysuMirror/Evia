import { expect, test } from '@playwright/test';
import type { ContentItem } from '../../packages/api-client/src/index';
import { decodeReplayPayloads, latestSnapshot } from '../../apps/console/src/session-replay/model';

function encoded(value: unknown) {
  return Buffer.from(JSON.stringify(value)).toString('base64url');
}
function item(content: string, itemID: string): ContentItem {
  return {
    content_type: 'session_replay_snapshot',
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
