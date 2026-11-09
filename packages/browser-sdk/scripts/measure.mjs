import { performance } from 'node:perf_hooks';
import { readdir, readFile } from 'node:fs/promises';
import { gzipSync } from 'node:zlib';
import { createEviaClient } from '../dist/index.js';

const projectId = '00000000-0000-4000-8000-000000000001';
const files = (await readdir(new URL('../dist/', import.meta.url))).filter((file) =>
  file.endsWith('.js'),
);
const contents = await Promise.all(
  files.map((file) => readFile(new URL(`../dist/${file}`, import.meta.url))),
);
const samples = [];
for (let iteration = 0; iteration < 5; iteration++) {
  const client = createEviaClient({
    endpoint: 'https://events.example',
    writeKey: 'public-measurement',
    projectId,
    environment: 'development',
    storage: null,
    lifecycle: false,
    fetch: async () =>
      new Response(
        JSON.stringify({
          format: 'evia.public-config.v1',
          project_id: projectId,
          environment: 'development',
          release_revision: 1,
          events: [{ event_key: 'item_opened', schema_version: 1, schema: { type: 'object' } }],
        }),
      ),
  });
  await client.init();
  const start = performance.now();
  const eventLoop = new Promise((resolve) =>
    setTimeout(() => resolve(performance.now() - start), 0),
  );
  for (let index = 0; index < 200; index++)
    client.emit('item_opened', { item_id: String(index), visible: true, ratio: 0.5 });
  const emitMs = performance.now() - start;
  let full = false;
  try {
    client.emit('item_opened');
  } catch (error) {
    full = error.code === 'QUEUE_FULL';
  }
  if (!full || client.pending !== 200)
    throw new Error('measurement did not enforce default queue bound');
  client.destroy();
  samples.push({ emit_200_ms: emitMs, event_loop_delay_ms: await eventLoop });
}
console.log(
  JSON.stringify(
    {
      node: process.version,
      modules: files.length,
      javascript_bytes: contents.reduce((sum, content) => sum + content.length, 0),
      javascript_gzip_bytes_separate_modules: contents.reduce(
        (sum, content) => sum + gzipSync(content).length,
        0,
      ),
      samples,
      note: 'Local Node measurement of one synchronous default-capacity burst and teardown; no browser, network or user-capacity claim.',
    },
    null,
    2,
  ),
);
