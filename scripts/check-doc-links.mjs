#!/usr/bin/env node

// Three repo-consistency checks, all run by `make docs-check`:
//
//   1. Every local Markdown link resolves. macOS does not distinguish file-name
//      case, so a wrong-case link is only caught here on a case-sensitive
//      filesystem — CI runs this on Linux, which is that filesystem.
//   2. Every repo file a Go test reads through a relative path exists. Eight
//      tests do this today (`os.ReadFile("../../../../remote/qubes-rpc/...")`);
//      renaming one of those files used to turn a green test into a runtime
//      failure with nothing to notice beforehand.
//   3. No Salt state tree lives in this repo. AGENTS.md §1 makes
//      qubes-salt-config the only source of Qubes-side Salt states, and a second
//      tree here drifts from the one that is actually applied:
//      `salt/qubes-air/vault-cloud` shipped a credential-serving qrexec service
//      that qubes-salt-config never installed, and a dom0 script told operators
//      to apply it. The only Salt files allowed are the flat pillar examples
//      directly in salt/pillar/ (`*.sls`, `*.sls.example`). Anything else under
//      salt/ fails, including a subdirectory of salt/pillar/, and so does a
//      `*.sls` or `*.top` file (or its `.example`) anywhere else. The salt/
//      prefix and the suffixes are matched without regard to case.
//
// Checks 2 and 3 live in this script rather than new ones because
// `make docs-check` (and the Docs workflow) run exactly the scripts they name,
// and all three are "does the tree say what the docs say" checks.

import { execFileSync } from 'node:child_process';
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

// Listed through git (tracked, plus untracked files that are not ignored) rather
// than by walking the disk: a checkout can contain other worktrees of this repo,
// and their salt/ trees are not part of this one. Git reports each nested
// worktree as a single directory entry and does not descend into it.
function repoFiles() {
  const listing = execFileSync(
    'git',
    ['ls-files', '-z', '--cached', '--others', '--exclude-standard'],
    { cwd: root, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 }
  );
  return [...new Set(listing.split('\0'))].filter(
    // Judge the working tree, like the link check: an index entry deleted on
    // disk is a removal in progress. CI checks a fresh checkout, which has it.
    (path) => path !== '' && existsSync(join(root, path))
  );
}

// The allowlist is the current shape of salt/: default.sls, top.sls and
// remotevm.sls.example, one level deep, exact case. Growing it (a pillar
// subdirectory, a README) means editing this pattern in the same commit.
const SALT_PILLAR_EXAMPLE = /^salt\/pillar\/[^/]+\.sls(?:\.example)?$/;
// Case-insensitive, so a rename that only changes case (Salt/, init.SLS) does
// not step around the check; on the default macOS filesystem init.SLS even
// opens as init.sls.
const SALT_DIR = /^salt\//i;
const SALT_FILE = /\.(?:sls|top)(?:\.example)?$/i;

// Anything under salt/ except the flat pillar examples, and a Salt file
// anywhere else: a tree moved out of salt/, or into a salt/pillar/
// subdirectory, is the same second entry point.
function isParallelSalt(path) {
  if (SALT_PILLAR_EXAMPLE.test(path)) {
    return false;
  }
  return SALT_DIR.test(path) || SALT_FILE.test(path);
}

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

function checkSaltBoundary() {
  let files;
  try {
    files = repoFiles();
  } catch (error) {
    // Fail closed: without the file list the boundary is unchecked, not clean.
    console.error(`Salt boundary: cannot list repo files through git: ${error.message}`);
    return false;
  }
  const parallelSalt = files.filter(isParallelSalt).sort();
  if (parallelSalt.length === 0) {
    console.log('Salt boundary: only the flat pillar examples in salt/pillar/');
    return true;
  }
  console.error(
    'Salt files outside the salt/pillar/*.sls[.example] allowlist (Qubes-side states belong in qubes-salt-config, AGENTS.md §1):'
  );
  for (const item of parallelSalt) {
    console.error(`  ${item}`);
  }
  return false;
}

if (!checkSaltBoundary()) {
  process.exitCode = 1;
}
