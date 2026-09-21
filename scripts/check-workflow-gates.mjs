#!/usr/bin/env node

// Guards the CI security gates against the two ways this repo has silently
// weakened them before:
//
//   1. `-no-fail` / `continue-on-error: true` on a scan step, which turns a
//      finding into a green check.
//   2. A floating action ref (`@master` / `@main`), so the code that runs can
//      change without any commit here.
//
// It is a line-based heuristic on purpose: a full YAML parse would need a
// dependency, and these patterns are structural enough to match reliably. It
// errs toward reporting, and every rule is listed in the output so a false
// positive is easy to see.

import { readFileSync, readdirSync } from 'node:fs';
import { join, resolve } from 'node:path';

const root = resolve(import.meta.dirname, '..');
const workflowsDir = join(root, '.github', 'workflows');

const SECURITY_STEP = /(gosec|govulncheck|vulnerab|trivy|gitleaks|secret|sast|codeql)/i;
const FLOATING_REF = /uses:\s*[^\s@]+@(master|main|HEAD)\b/;

const problems = [];

function checkWorkflow(file, lines) {
  lines.forEach((raw, i) => {
    const line = raw.trim();
    const where = `${file}:${i + 1}`;

    // Comments are how we explain these rules; do not flag them.
    if (line.startsWith('#')) return;

    if (line.includes('-no-fail')) {
      problems.push(`${where} uses -no-fail, which lets a security finding pass`);
    }
    if (FLOATING_REF.test(line)) {
      problems.push(`${where} pins an action to a floating branch: ${line}`);
    }
    if (line.startsWith('continue-on-error: true')) {
      // Look back a little for the step's name.
      const window = lines.slice(Math.max(0, i - 8), i).join('\n');
      if (SECURITY_STEP.test(window)) {
        problems.push(`${where} sets continue-on-error on a security scan step`);
      }
    }
  });
}

for (const entry of readdirSync(workflowsDir)) {
  if (!entry.endsWith('.yml') && !entry.endsWith('.yaml')) continue;
  const file = join('.github', 'workflows', entry);
  checkWorkflow(file, readFileSync(join(workflowsDir, entry), 'utf8').split('\n'));
}

if (problems.length > 0) {
  console.error('CI gate violations:');
  for (const p of problems) console.error(`  ${p}`);
  process.exitCode = 1;
} else {
  console.log('CI gates: no -no-fail, no floating action refs, no bypassed security scans');
}
