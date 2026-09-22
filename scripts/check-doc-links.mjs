#!/usr/bin/env node

// Two local-reference checks, both run by `make docs-check`:
//
//   1. Every local Markdown link resolves. macOS does not distinguish file-name
//      case, so a wrong-case link is only caught here on a case-sensitive
//      filesystem — CI runs this on Linux, which is that filesystem.
//   2. Every repo file a Go test reads through a relative path exists. Eight
//      tests do this today (`os.ReadFile("../../../../remote/qubes-rpc/...")`);
//      renaming one of those files used to turn a green test into a runtime
//      failure with nothing to notice beforehand.
//
// It lives in this script rather than a new one because `make docs-check` runs
// exactly the scripts named in the Makefile, and this is the reference-existence
// check.

import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';

const root = resolve(import.meta.dirname, '..');
const ignoredDirectories = new Set([
  '.git',
  '.terraform',
  'dist',
  'node_modules',
  'vendor'
]);

function filesUnder(directory, keep) {
  const files = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (!ignoredDirectories.has(entry.name)) {
        files.push(...filesUnder(join(directory, entry.name), keep));
      }
      continue;
    }
    if (entry.isFile() && keep(entry.name)) {
      files.push(join(directory, entry.name));
    }
  }
  return files;
}

const markdownFiles = (directory) => filesUnder(directory, (name) => name.endsWith('.md'));
const goTestFiles = (directory) => filesUnder(directory, (name) => name.endsWith('_test.go'));

function localTargets(markdown) {
  const targets = [];
  const link = /!?\[[^\]]*\]\((<[^>]+>|[^\s)]+)(?:\s+["'][^)]*["'])?\)/g;
  for (const match of markdown.matchAll(link)) {
    let target = match[1];
    if (target.startsWith('<') && target.endsWith('>')) {
      target = target.slice(1, -1);
    }
    if (
      target === '' ||
      target.startsWith('#') ||
      /^(?:https?:|mailto:|data:)/i.test(target)
    ) {
      continue;
    }
    target = target.split('#', 1)[0].split('?', 1)[0];
    if (target !== '') {
      targets.push(decodeURIComponent(target));
    }
  }
  return targets;
}

// Only the calls that READ a path are matched, and only when the path walks up
// out of the test's own directory. A bare `"../..."` string is usually a
// path-TRAVERSAL input (`"../firefox"`, `s.path("../../etc/passwd")`), where
// requiring the target to exist would be nonsense — there are 10 of those and
// they must stay unreported.
const FIXTURE_READ = /(?:os\.ReadFile|os\.Open|ioutil\.ReadFile|filepath\.Abs)\(\s*"((?:\.\.\/)+[^"]*)"\s*\)/g;

const missing = [];
let checked = 0;
for (const file of markdownFiles(root)) {
  for (const target of localTargets(readFileSync(file, 'utf8'))) {
    checked += 1;
    if (!existsSync(resolve(dirname(file), target))) {
      missing.push(`${file.slice(root.length + 1)} -> ${target}`);
    }
  }
}

const missingFixtures = [];
let fixturesChecked = 0;
for (const file of goTestFiles(root)) {
  for (const match of readFileSync(file, 'utf8').matchAll(FIXTURE_READ)) {
    fixturesChecked += 1;
    if (!existsSync(resolve(dirname(file), match[1]))) {
      missingFixtures.push(`${file.slice(root.length + 1)} -> ${match[1]}`);
    }
  }
}

if (missing.length > 0) {
  console.error('Missing local Markdown links:');
  for (const item of missing) {
    console.error(`  ${item}`);
  }
  process.exitCode = 1;
} else {
  console.log(`Markdown links: ${checked} local targets OK`);
}

if (missingFixtures.length > 0) {
  console.error('Missing repo files read by Go tests:');
  for (const item of missingFixtures) {
    console.error(`  ${item}`);
  }
  process.exitCode = 1;
} else {
  console.log(`Test fixture references: ${fixturesChecked} repo files OK`);
}
