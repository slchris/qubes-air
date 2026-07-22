#!/usr/bin/env node

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

function markdownFiles(directory) {
  const files = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (!ignoredDirectories.has(entry.name)) {
        files.push(...markdownFiles(join(directory, entry.name)));
      }
      continue;
    }
    if (entry.isFile() && entry.name.endsWith('.md')) {
      files.push(join(directory, entry.name));
    }
  }
  return files;
}

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

if (missing.length > 0) {
  console.error('Missing local Markdown links:');
  for (const item of missing) {
    console.error(`  ${item}`);
  }
  process.exitCode = 1;
} else {
  console.log(`Markdown links: ${checked} local targets OK`);
}
