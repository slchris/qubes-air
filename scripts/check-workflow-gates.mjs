#!/usr/bin/env node

// Guards the CI security gates against the three ways this repo has silently
// weakened them before:
//
//   1. `-no-fail` / `continue-on-error: true` on a scan step, which turns a
//      finding into a green check.
//   2. A floating action ref (`@master` / `@main`), so the code that runs can
//      change without any commit here.
//   3. `|| true` at the end of a `run:` command, which discards the exit status
//      so the step is green whatever the tool reported. `AGENTS.md` §2.4 names
//      this construct as forbidden; before this rule the repo shipped two of
//      them (dependency.yml) with nothing to notice.
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
// `cmd || true`, and the same discard spelled `|| :`. Matched anywhere on a
// non-comment line: a trailing `|| true; echo ok` discards a status just as
// thoroughly as the bare form, so there is no reason to allow it.
const DISCARDED_STATUS = /\|\|\s*(true|:)(?=\s|$)/;
// A step is a list item under `steps:` whose first key is one of these. Anchored
// on the key so a `- ` line inside a `run: |` block cannot be mistaken for a step
// boundary, which would shorten the window below and hide a finding.
const STEP_START = /^\s*-\s+(?:name|id|uses|run|if|shell|working-directory|env|with|continue-on-error):/;

const problems = [];

function checkWorkflow(file, lines) {
  let stepStart = 0;
  lines.forEach((raw, i) => {
    const line = raw.trim();
    const where = `${file}:${i + 1}`;

    // Comments are how we explain these rules; do not flag them.
    if (line.startsWith('#')) return;

    if (STEP_START.test(raw)) stepStart = i;

    if (line.includes('-no-fail')) {
      problems.push(`${where} uses -no-fail, which lets a security finding pass`);
    }
    if (FLOATING_REF.test(line)) {
      problems.push(`${where} pins an action to a floating branch: ${line}`);
    }
    if (DISCARDED_STATUS.test(line)) {
      const discarded = line.match(DISCARDED_STATUS);
      problems.push(`${where} discards the command's exit status with "|| ${discarded[1]}", so the step cannot fail`);
    }
    if (line.startsWith('continue-on-error: true')) {
      // Look back to the start of THIS step for its name. A fixed-size window
      // used to reach into the step above: it reported the wrong step and could
      // just as easily scroll past the name of the right one.
      const step = lines.slice(stepStart, i + 1).join('\n');
      if (SECURITY_STEP.test(step)) {
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
  // Every rule is listed, so a run that goes green says which four things it
  // actually checked instead of implying a broader guarantee than it gives.
  console.log(
    'CI gates: no -no-fail, no "|| true", no floating action refs, no continue-on-error on scan steps',
  );
}
