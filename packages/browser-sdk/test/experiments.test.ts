import assert from 'node:assert/strict';
import test from 'node:test';
import {
  assignExperimentVariant,
  createEviaClient,
  hashExperimentBucketBps,
  type ConfigExperiment,
  type PublicConfig,
} from '../src/index.js';

const projectId = '00000000-0000-4000-8000-000000000001';

test('deterministic bucketing is uniform and respects overrides and partial rollout', () => {
  const experiment: ConfigExperiment = {
    experiment_key: 'home_ranking',
    version: 3,
    salt: 'salt-2026',
    control_variant: 'time',
    exposure_event_key: 'experiment_exposed',
    variants: [
      { key: 'time', weight_bps: 5000 },
      { key: 'recommend', weight_bps: 5000 },
    ],
    overrides: { 'vip-user': 'recommend' },
  };

  assert.deepEqual(assignExperimentVariant(experiment, 'vip-user'), {
    variant: 'recommend',
    allocated: true,
    reason: 'override',
  });

  // Deterministic across repeated calls
  const first = assignExperimentVariant(experiment, 'user-42');
  const second = assignExperimentVariant(experiment, 'user-42');
  assert.deepEqual(first, second);

  // Uniformity over 2000 users (~50/50 within +/-5%)
  let recommendCount = 0;
  for (let i = 0; i < 2000; i++) {
    const b = hashExperimentBucketBps(experiment.salt, experiment.experiment_key, `u-${i}`);
    assert.ok(b >= 0 && b < 10000);
    if (assignExperimentVariant(experiment, `u-${i}`).variant === 'recommend') recommendCount++;
  }
  assert.ok(
    recommendCount > 900 && recommendCount < 1100,
    `expected ~1000 recommend assignments, got ${recommendCount}`,
  );

  // Partial traffic allocation (20% total) leaves ~80% unallocated falling back to control
  const partial: ConfigExperiment = {
    ...experiment,
    overrides: undefined,
    variants: [
      { key: 'time', weight_bps: 1000 },
      { key: 'recommend', weight_bps: 1000 },
    ],
  };
  let unallocated = 0;
  for (let i = 0; i < 1000; i++) {
    const res = assignExperimentVariant(partial, `p-${i}`);
    if (!res.allocated) {
      assert.equal(res.reason, 'unallocated');
      assert.equal(res.variant, 'time');
      unallocated++;
    }
  }
  assert.ok(
    unallocated > 700 && unallocated < 900,
    `expected ~800 unallocated, got ${unallocated}`,
  );
});

test('evaluateExperiment auto-exposes once per session/subject/version and dedups', async () => {
  const config: PublicConfig = {
    format: 'evia.public-config.v1',
    project_id: projectId,
    environment: 'development',
    release_revision: 1,
    events: [
      {
        event_key: 'experiment_exposed',
        schema_version: 1,
        schema: {
          type: 'object',
          additionalProperties: false,
          required: ['experiment_key', 'variant'],
          properties: {
            experiment_key: { type: 'string' },
            variant: { type: 'string' },
          },
        },
      },
    ],
    experiments: [
      {
        experiment_key: 'home_ranking',
        version: 2,
        salt: 'fixed-salt',
        control_variant: 'time',
        exposure_event_key: 'experiment_exposed',
        variants: [
          { key: 'time', weight_bps: 5000 },
          { key: 'recommend', weight_bps: 5000 },
        ],
        overrides: { 'u-100': 'recommend' },
      },
    ],
  };

  const collectedBodies: unknown[] = [];
  const fakeFetch: typeof fetch = async (input, init) => {
    const url = String(input);
    if (url.includes('/v1/config')) {
      return new Response(JSON.stringify(config), {
        status: 200,
        headers: { 'content-type': 'application/json', etag: '"rev1"' },
      });
    }
    const body = JSON.parse(String(init?.body ?? '{}')) as { events: { event_id: string }[] };
    collectedBodies.push(body);
    return new Response(
      JSON.stringify({
        receipt_id: 'r-1',
        accepted: body.events.length,
        rejected: 0,
        results: body.events.map((e) => ({ event_id: e.event_id, status: 'accepted' })),
      }),
      { status: 200, headers: { 'content-type': 'application/json' } },
    );
  };

  const client = createEviaClient({
    endpoint: 'https://evia.example.test',
    writeKey: 'wk_test_1234567890',
    projectId,
    environment: 'development',
    fetch: fakeFetch,
    storage: null,
    lifecycle: false,
  });

  await client.init();
  assert.equal(client.listExperiments().length, 1);

  client.identify('u-100');
  const first = client.evaluateExperiment('home_ranking');
  assert.equal(first.variant, 'recommend');
  assert.equal(first.reason, 'override');
  assert.equal(first.allocated, true);
  assert.ok(first.exposure, 'first evaluation should emit exposure');

  // Second call for same user/version must not emit duplicate exposure
  const second = client.evaluateExperiment('home_ranking');
  assert.equal(second.variant, 'recommend');
  assert.equal(second.exposure, undefined);

  // Unknown experiment returns fallback without throwing
  const missing = client.evaluateExperiment('unknown_exp', { fallback: 'default_v' });
  assert.equal(missing.variant, 'default_v');
  assert.equal(missing.reason, 'not_found');
  assert.equal(missing.allocated, false);

  await client.flush();
  assert.equal(collectedBodies.length, 1);
  const sent = collectedBodies[0] as {
    events: { event_key: string; properties: Record<string, unknown> }[];
  };
  assert.equal(sent.events.length, 1);
  assert.equal(sent.events[0]!.event_key, 'experiment_exposed');
  assert.deepEqual(sent.events[0]!.properties, {
    experiment_key: 'home_ranking',
    variant: 'recommend',
  });

  client.destroy();
});
