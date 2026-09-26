#!/usr/bin/env node

// Guards the CI security gates against the ways this repo has silently
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
//   4. An action release that still runs on Node 20. GitHub is retiring that
//      runtime on its hosted runners (removal on 2026-09-16 according to the
//      gitleaks-action v3.0.0 release notes -- a vendor statement, not verified
//      here), after which such a step would not run; a revert of the Node 24
//      upgrade would otherwise only surface as a broken CI run.
//
// It is a line-based heuristic on purpose: a full YAML parse would need a
// dependency, and these patterns are structural enough to match reliably. It
// errs toward reporting, and every rule is listed in the output so a false
// positive is easy to see.

import { readFileSync, readdirSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';

const root = resolve(import.meta.dirname, '..');
// The repo's workflows by default. A directory argument exists for the tests in
// check-workflow-gates.test.mjs, which run this script against fixture files
// to prove each rule actually fails the run.
const workflowsDir = process.argv[2] ? resolve(process.argv[2]) : join(root, '.github', 'workflows');

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

// `uses: owner/repo[/path]@ref  # optional release comment`.
const USES = /^(?:-\s+)?uses:\s*["']?([^\s@"']+)@([^\s"'#]+)["']?(?:\s+#\s*(\S+))?/;
const VERSION = /^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?$/;
const COMMIT_SHA = /^[0-9a-f]{40}$/;

// First release of each action this repo uses whose action.yml declares
// `runs.using: node24` (for the composite trivy-action: whose nested actions
// all do). Every earlier release runs on Node 20 or older. Each entry was
// checked by reading action.yml at that tag and at the release before it.
const NODE24_FIRST_RELEASE = new Map([
  ['actions/checkout', [5, 0, 0]],
  // Not v6.0.0: its release notes announce Node 24, but action.yml in v6.0.0
  // and v6.1.0 still declares node20. It changed in v6.2.0 (setup-go#691).
  ['actions/setup-go', [6, 2, 0]],
  ['actions/setup-node', [5, 0, 0]],
  ['actions/setup-python', [6, 0, 0]],
  // v5 still declares node20.
  ['actions/upload-artifact', [6, 0, 0]],
  // v4 kept v3's minor numbering; its first release is v4.30.7.
  ['github/codeql-action', [4, 30, 7]],
  ['golangci/golangci-lint-action', [9, 0, 0]],
  ['softprops/action-gh-release', [3, 0, 0]],
  ['gitleaks/gitleaks-action', [3, 0, 0]],
  ['aquasecurity/trivy-action', [0, 36, 0]],
]);

// A SHA-pinned ref carries no version of its own. The release comment next to
// it normally does, but a hand edit can drop it, so the Node 20 commits this
// repo has actually pinned are also recognized by SHA.
const NODE20_COMMITS = new Map([
  ['ff98106e4c7b2bc287b24eaf42907196329070c7', 'gitleaks/gitleaks-action v2.3.9'],
  ['57a97c7e7821a5776cebc9bb87c984fa69cba8f1', 'aquasecurity/trivy-action 0.35.0'],
]);

// The numeric parts a version spells out: `v6` -> [6], `v6.2.0` -> [6, 2, 0].
function parseVersion(text) {
  const m = VERSION.exec(text ?? '');
  return m ? m.slice(1).filter((part) => part !== undefined).map(Number) : null;
}

// Whether `version` names a release before `first`, comparing only the parts
// `version` spells out.
function olderThan(version, first) {
  for (let i = 0; i < version.length; i += 1) {
    if (version[i] !== first[i]) return version[i] < first[i];
  }
  return false;
}

// The version a `uses:` ref stands for, or null when it cannot be told.
// - A tag (`@v6`, `@v6.2.0`) is judged on the parts it names. A major-only tag
//   such as `@v6` is the floating tag that moves to the newest v6 release, so
//   `@v6` passes once any v6 release runs on Node 24, while `@v6.1.0` is
//   judged as exactly v6.1.0.
// - A commit SHA is judged by its `# vX.Y.Z` release comment. The commit is
//   fixed, so a partial comment (`# v6`) could be any release in that line and
//   is read as the lowest one (v6.0.0). That fails closed; write the full
//   version.
function releaseOf(ref, comment) {
  if (!COMMIT_SHA.test(ref)) return parseVersion(ref);
  const version = parseVersion(comment);
  return version && [0, 1, 2].map((i) => version[i] ?? 0);
}

function node20Release(line) {
  const m = USES.exec(line);
  if (!m) return null;
  const [, action, ref, comment] = m;
  const repo = action.split('/').slice(0, 2).join('/');
  if (NODE20_COMMITS.has(ref)) return NODE20_COMMITS.get(ref);
  const first = NODE24_FIRST_RELEASE.get(repo);
  if (!first) return null;
  const version = releaseOf(ref, comment);
  if (version && olderThan(version, first)) {
    const named = COMMIT_SHA.test(ref) ? `${ref.slice(0, 12)} # ${comment}` : ref;
    return `${repo}@${named} (first Node 24 release: v${first.join('.')})`;
  }
  return null;
}

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
    const node20 = node20Release(line);
    if (node20) {
      problems.push(`${where} uses an action release that still runs on Node 20: ${node20}`);
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

let checked = 0;
for (const entry of readdirSync(workflowsDir).sort()) {
  if (!entry.endsWith('.yml') && !entry.endsWith('.yaml')) continue;
  const file = relative(root, join(workflowsDir, entry));
  checkWorkflow(file, readFileSync(join(workflowsDir, entry), 'utf8').split('\n'));
  checked += 1;
}
// Reading nothing is not a pass: a moved or emptied workflow directory would
// otherwise print the green line below having checked zero files.
if (checked === 0) {
  problems.push(`${relative(root, workflowsDir) || '.'} contains no workflow files to check`);
}

if (problems.length > 0) {
  console.error('CI gate violations:');
  for (const p of problems) console.error(`  ${p}`);
  process.exitCode = 1;
} else {
  // Every rule is listed, so a run that goes green says which five things it
  // actually checked instead of implying a broader guarantee than it gives.
  console.log(
    'CI gates: no -no-fail, no "|| true", no floating action refs, no continue-on-error on scan steps, ' +
      'no Node 20 action releases',
  );
}
