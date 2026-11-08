#!/usr/bin/env node
'use strict';

const path = require('node:path');
const ts = require('typescript');

const sourceExtension = /\.(?:[cm]?ts|tsx|[cm]?js|jsx)$/;
const canonical = (file) => path.normalize(ts.sys.realpath?.(file) ?? file);
const inside = (root, file) => {
  const relative = path.relative(root, file);
  return relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative);
};
const sourceFile = (root, file) =>
  inside(root, file) && sourceExtension.test(file) && !/\.d\.[cm]?ts$/.test(file) &&
  !file.split(path.sep).includes('node_modules');

function moduleReferences(source) {
  const references = [];
  function add(literal) {
    if (literal && (ts.isStringLiteral(literal) || ts.isNoSubstitutionTemplateLiteral(literal)))
      references.push({ specifier: literal.text, position: literal.getStart(source) });
  }
  function visit(node) {
    if (ts.isImportDeclaration(node)) {
      add(node.moduleSpecifier);
    } else if (ts.isExportDeclaration(node)) {
      add(node.moduleSpecifier);
    } else if (ts.isImportEqualsDeclaration(node) && ts.isExternalModuleReference(node.moduleReference)) {
      add(node.moduleReference.expression);
    } else if (ts.isImportTypeNode(node) && ts.isLiteralTypeNode(node.argument)) {
      add(node.argument.literal);
    } else if (ts.isCallExpression(node) && (node.expression.kind === ts.SyntaxKind.ImportKeyword ||
      ts.isIdentifier(node.expression) && node.expression.text === 'require')) {
      add(node.arguments[0]);
    }
    ts.forEachChild(node, visit);
  }
  visit(source);
  return references;
}

function forbiddenDependency(importer, specifier, target) {
  const react = /^(?:react|react-dom)(?:\/|$)/.test(specifier);
  const design = /^@evia\/design-system(?:\/|$)/.test(specifier) || target.startsWith('packages/design-system/');
  const client = /^@evia\/api-client(?:\/|$)/.test(specifier) || target.startsWith('packages/api-client/');
  const application = /^@evia\/console(?:\/|$)/.test(specifier) || target.startsWith('apps/');
  if (importer.startsWith('packages/design-system/') && (client || application))
    return 'design-system cannot depend on application or api-client business code';
  if (importer.startsWith('packages/api-client/') && (react || design || application))
    return 'api-client cannot depend on React, design-system or applications';
  if (importer.startsWith('packages/sdk/') && (react || design || application))
    return 'sdk must remain independent of React, design-system and applications';
}

/** Check configured source roots and their resolved local dependencies, without emitting files. */
function checkBoundaries(root) {
  root = canonical(path.resolve(root));
  const configPath = ts.findConfigFile(root, ts.sys.fileExists, 'tsconfig.json');
  if (!configPath) throw new Error(`Missing tsconfig.json in ${root}`);
  const loaded = ts.readConfigFile(configPath, ts.sys.readFile);
  if (loaded.error) throw new Error(ts.flattenDiagnosticMessageText(loaded.error.messageText, '\n'));
  const config = ts.parseJsonConfigFileContent(loaded.config, ts.sys, path.dirname(configPath));
  if (config.errors.length) throw new Error(config.errors.map((error) => ts.flattenDiagnosticMessageText(error.messageText, '\n')).join('\n'));
  const resolutionCache = ts.createModuleResolutionCache(root, ts.sys.useCaseSensitiveFileNames ? (file) => file : (file) => file.toLowerCase(), config.options);
  const graph = new Map(), diagnostics = [];
  const pending = config.fileNames.map(canonical).filter((file) => sourceFile(root, file));
  const relative = (file) => path.relative(root, file).split(path.sep).join('/');
  const seen = new Set();
  for (let index = 0; index < pending.length; index += 1) {
    const file = pending[index];
    if (seen.has(file)) continue;
    seen.add(file);
    const text = ts.sys.readFile(file);
    if (text === undefined) throw new Error(`Cannot read ${relative(file)}`);
    const source = ts.createSourceFile(file, text, config.options.target ?? ts.ScriptTarget.Latest, true);
    // Follow the configured compiler's import elision, including mixed bindings and
    // values used only in type positions. No generated JavaScript is written.
    const emitted = ts.transpileModule(text, {
      fileName: file,
      compilerOptions: { ...config.options, noEmit: false, declaration: false,
        emitDeclarationOnly: false, sourceMap: false, inlineSourceMap: false },
    }).outputText;
    const runtimeReferences = moduleReferences(ts.createSourceFile(`${file}.js`, emitted,
      config.options.target ?? ts.ScriptTarget.Latest, true, ts.ScriptKind.JS));
    const runtimeSpecifiers = new Set(runtimeReferences.map((reference) => reference.specifier));
    const references = new Map(moduleReferences(source).map((reference) => [reference.specifier, reference]));
    for (const reference of runtimeReferences)
      if (!references.has(reference.specifier)) references.set(reference.specifier, { ...reference, position: 0 });
    const edges = new Map();
    graph.set(file, edges);
    for (const reference of references.values()) {
      const resolution = ts.resolveModuleName(reference.specifier, file, config.options, ts.sys, resolutionCache).resolvedModule;
      const target = resolution ? canonical(resolution.resolvedFileName) : undefined;
      const violation = forbiddenDependency(relative(file), reference.specifier, target && inside(root, target) ? relative(target) : '');
      const line = source.getLineAndCharacterOfPosition(reference.position).line + 1;
      if (violation) diagnostics.push(`${relative(file)}:${line}: ${violation} (${reference.specifier})`);
      if (target && sourceFile(root, target)) {
        pending.push(target);
        if (runtimeSpecifiers.has(reference.specifier)) edges.set(target, line);
      }
    }
  }
  const visited = new Set(), visiting = new Map(), route = [];
  function visit(file) {
    if (visited.has(file)) return;
    visiting.set(file, route.length); route.push(file);
    for (const [target, line] of [...graph.get(file)].sort(([left], [right]) => left.localeCompare(right))) {
      if (visiting.has(target)) {
        const cycle = [...route.slice(visiting.get(target)), target].map(relative);
        diagnostics.push(`${relative(file)}:${line}: runtime import cycle: ${cycle.join(' -> ')}`);
      } else visit(target);
    }
    route.pop(); visiting.delete(file); visited.add(file);
  }
  for (const file of [...graph.keys()].sort()) visit(file);
  return { diagnostics, files: graph.size, runtimeEdges: [...graph.values()].reduce((total, edges) => total + edges.size, 0) };
}

module.exports = { checkBoundaries };
if (require.main === module) {
  try {
    const result = checkBoundaries(path.resolve(__dirname, '..'));
    if (result.diagnostics.length) {
      console.error(result.diagnostics.join('\n')); process.exitCode = 1;
    } else console.log(`Frontend boundaries verified: ${result.files} source modules, ${result.runtimeEdges} runtime imports, no cycles.`);
  } catch (error) {
    console.error(error.message); process.exitCode = 1;
  }
}
