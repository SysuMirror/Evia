'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { test } = require('node:test');
const { checkBoundaries } = require('./check_boundaries.cjs');

function fixture(t, files, options = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'evia-boundaries-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.writeFileSync(path.join(root, 'tsconfig.json'), JSON.stringify({
    compilerOptions: {
      target: 'ES2022', module: 'ESNext', moduleResolution: 'Bundler',
      jsx: 'react-jsx', isolatedModules: true, baseUrl: '.',
      paths: {
        '@evia/api-client': ['packages/api-client/index.ts'],
        '@evia/design-system': ['packages/design-system/index.ts'],
        '@business': ['packages/api-client/index.ts'],
        '@local/*': ['apps/console/src/*'],
      },
      ...options,
    },
    include: ['apps', 'packages'],
  }));
  for (const [file, content] of Object.entries(files)) {
    const target = path.join(root, file);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, content);
  }
  return checkBoundaries(root);
}

test('console can import the shared design package', (t) => {
  const result = fixture(t, {
    'packages/design-system/index.ts': 'export const Button = 1;',
    'apps/console/src/a.ts': 'import { Button } from "@evia/design-system"; export const a = Button;',
  });
  assert.deepEqual(result.diagnostics, []);
  assert.equal(result.runtimeEdges, 1);
});

test('design-system rejects type-only business imports through a configured alias', (t) => {
  const result = fixture(t, {
    'packages/api-client/index.ts': 'export type T = string;',
    'packages/design-system/index.ts': 'import type { T } from "@business"; export type A = T;',
  });
  assert.equal(result.runtimeEdges, 0);
  assert.match(result.diagnostics.join('\n'), /design-system cannot.*@business/);
});

test('api-client rejects React type re-exports without requiring React to resolve', (t) => {
  const result = fixture(t, {
    'packages/api-client/index.ts': 'export type { ReactNode } from "react";',
  });
  assert.match(result.diagnostics.join('\n'), /api-client cannot depend on React/);
});

test('SDK rejects relative application imports when the SDK directory exists', (t) => {
  const result = fixture(t, {
    'apps/console/src/a.ts': 'export const a = 1;',
    'packages/sdk/index.ts': 'import { a } from "../../apps/console/src/a"; export const b = a;',
  });
  assert.match(result.diagnostics.join('\n'), /sdk must remain independent/);
});

test('a mixed import retains its value dependency but not a type-only return edge', (t) => {
  const result = fixture(t, {
    'apps/console/src/a.ts': 'import { type Label, value } from "./b"; export const a = value; export type A = Label;',
    'apps/console/src/b.ts': 'import type { A } from "./a"; export type Label = string; export const value = 1;',
  });
  assert.deepEqual(result.diagnostics, []);
  assert.equal(result.runtimeEdges, 1);
});

test('all-type named bindings do not create a runtime cycle', (t) => {
  const result = fixture(t, {
    'apps/console/src/a.ts': 'import { type Label } from "./b"; export type A = Label; export const a = 1;',
    'apps/console/src/b.ts': 'import { a } from "./a"; export type Label = string; export const value = a;',
  });
  assert.deepEqual(result.diagnostics, []);
  assert.equal(result.runtimeEdges, 1);
});

test('compiler-elided imports used only in type positions do not create cycles', (t) => {
  const result = fixture(t, {
    'apps/console/src/a.ts': 'import { Label } from "./b"; export type A = Label; export const a = 1;',
    'apps/console/src/b.ts': 'import { a } from "./a"; export type Label = string; export const value = a;',
  });
  assert.deepEqual(result.diagnostics, []);
});

test('a value binding in a mixed import and a re-export form a real cycle', (t) => {
  const result = fixture(t, {
    'apps/console/src/a.ts': 'import { type Label, value } from "./b"; export const a = value;',
    'apps/console/src/b.ts': 'export { a as value } from "./a"; export type Label = string;',
  });
  assert.equal(result.runtimeEdges, 2);
  assert.match(result.diagnostics.join('\n'), /runtime import cycle: .*a\.ts -> .*b\.ts -> .*a\.ts/);
});

for (const literal of ['"./b"', '`./b`']) {
  test(`literal dynamic import ${literal} participates in cycle detection`, (t) => {
    const result = fixture(t, {
      'apps/console/src/a.ts': `export const load = () => import(${literal});`,
      'apps/console/src/b.ts': 'export * from "./a";',
    });
    assert.match(result.diagnostics.join('\n'), /runtime import cycle/);
  });
}

test('local cycle resolution honors tsconfig path aliases', (t) => {
  const result = fixture(t, {
    'apps/console/src/a.ts': 'import { b } from "@local/b"; export const a = b;',
    'apps/console/src/b.ts': 'import { a } from "@local/a"; export const b = a;',
  });
  assert.match(result.diagnostics.join('\n'), /runtime import cycle/);
});

test('type import expressions still obey package boundaries', (t) => {
  const result = fixture(t, {
    'packages/api-client/index.ts': 'export type T = string;',
    'packages/design-system/index.ts': 'export type A = typeof import("@evia/api-client");',
  });
  assert.match(result.diagnostics.join('\n'), /design-system cannot/);
  assert.equal(result.runtimeEdges, 0);
});

test('literal CommonJS dependencies participate in cycle detection', (t) => {
  const result = fixture(t, {
    'apps/console/src/a.ts': 'export const a = require("./b").b;',
    'apps/console/src/b.ts': 'export const b = require("./a").a;',
  }, { module: 'CommonJS', moduleResolution: 'Node' });
  assert.match(result.diagnostics.join('\n'), /runtime import cycle/);
});

test('verbatim module semantics retain side effects of all-type named bindings', (t) => {
  const result = fixture(t, {
    'apps/console/src/a.ts': 'import { type B } from "./b"; export type A = string;',
    'apps/console/src/b.ts': 'import { type A } from "./a"; export type B = string;',
  }, { verbatimModuleSyntax: true });
  assert.match(result.diagnostics.join('\n'), /runtime import cycle/);
});

test('computed dynamic imports remain an explicit static-analysis limitation', (t) => {
  const result = fixture(t, {
    'apps/console/src/a.ts': 'export const load = (name: string) => import(name);',
    'apps/console/src/b.ts': 'export * from "./a";',
  });
  assert.deepEqual(result.diagnostics, []);
  assert.equal(result.runtimeEdges, 1);
});
