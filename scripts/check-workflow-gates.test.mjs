// Runs scripts/check-workflow-gates.mjs as CI does (a child process, judged by
// its exit status) against fixture workflows, so each rule is shown to fail the
// run rather than only to print something.
// Run: node --test scripts/check-workflow-gates.test.mjs

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { after, describe, test } from 'node:test';

const script = resolve(import.meta.dirname, 'check-workflow-gates.mjs');
const scratch = mkdtempSync(join(tmpdir(), 'workflow-gates-'));
after(() => rmSync(scratch, { recursive: true, force: true }));

let fixtures = 0;

function runGate(dir) {
  const run = spawnSync(process.execPath, dir ? [script, dir] : [script], { encoding: 'utf8' });
  return { status: run.status, output: run.stdout + run.stderr };
}

// One workflow file whose single job runs the given step lines.
function gateOnSteps(...steps) {
  fixtures += 1;
  const dir = join(scratch, `case-${fixtures}`);
  const workflow = [
    'name: fixture',
    'on: push',
    'jobs:',
    '  job:',
    '    runs-on: ubuntu-24.04',
    '    steps:',
    ...steps.map((step) => `      ${step}`),
    '',
  ].join('\n');
  mkdirSync(dir);
  writeFileSync(join(dir, 'fixture.yml'), workflow);
  return runGate(dir);
}

describe('check-workflow-gates.mjs', () => {
  test('the repository workflows pass every rule', () => {
    const { status, output } = runGate();
    assert.equal(status, 0, output);
    assert.match(output, /no Node 20 action releases/);
  });

  // Adjacent releases of each action: the last whose action.yml declares node20
  // and the first that declares node24 (for the composite trivy-action, in its
  // nested actions). Each was read from action.yml at that tag. Rejecting the
  // first column and accepting the second pins every entry of the script's
  // table from both sides: moving any entry by one release fails a test.
  const switchover = [
    ['actions/checkout', 'v4.4.0', 'v5.0.0'],
    // v6.0.0 and v6.1.0 still declare node20 despite v6.0.0's release notes.
    ['actions/setup-go', 'v6.1.0', 'v6.2.0'],
    ['actions/setup-node', 'v4.4.0', 'v5.0.0'],
    ['actions/setup-python', 'v5.6.0', 'v6.0.0'],
    ['actions/upload-artifact', 'v5.0.0', 'v6.0.0'],
    ['github/codeql-action/init', 'v3.38.2', 'v4.30.7'],
    ['golangci/golangci-lint-action', 'v8.0.0', 'v9.0.0'],
    ['softprops/action-gh-release', 'v2.6.2', 'v3.0.0'],
    ['gitleaks/gitleaks-action', 'v2.3.9', 'v3.0.0'],
    ['aquasecurity/trivy-action', 'v0.35.0', 'v0.36.0'],
  ];

  // Real tag commits of actions/setup-go, for SHA-pinned forms.
  const setupGo = {
    'v6.0.0': '44694675825211faa026b3c33043df3e48a5fa00',
    'v6.1.0': '4dc6199c7b1a012772edbd06daecab0f50c9053c',
    'v6.2.0': '7a3fe6cf4cb3a834922a1244abfce67bcef6a0c5',
  };

  function rejected(step) {
    const { status, output } = gateOnSteps(step);
    assert.equal(status, 1, output);
    assert.match(output, /still runs on Node 20/);
  }

  function accepted(step) {
    const { status, output } = gateOnSteps(step);
    assert.equal(status, 0, output);
  }

  describe('judges a full release version against the first Node 24 release', () => {
    for (const [action, lastNode20, firstNode24] of switchover) {
      test(`rejects ${action}@${lastNode20}`, () => rejected(`- uses: ${action}@${lastNode20}`));
      test(`accepts ${action}@${firstNode24}`, () => accepted(`- uses: ${action}@${firstNode24}`));
    }
    test('rejects actions/setup-go@v6.0.0', () => rejected('- uses: actions/setup-go@v6.0.0'));
    // A floating minor tag names the newest v6.1.x, which is still node20.
    test('rejects actions/setup-go@v6.1', () => rejected('- uses: actions/setup-go@v6.1'));
  });

  describe('judges a floating major tag by its major alone', () => {
    const cases = [
      ['rejects', '- uses: actions/checkout@v4'],
      ['rejects', '- uses: actions/setup-go@v5'],
      ['rejects', '- uses: actions/setup-node@v4'],
      ['rejects', '- uses: actions/setup-python@v5'],
      // v5 of upload-artifact still declares node20; v6 is the first node24.
      ['rejects', '- uses: actions/upload-artifact@v5'],
      ['rejects', '- uses: github/codeql-action/init@v3'],
      ['rejects', '- uses: golangci/golangci-lint-action@v8'],
      ['rejects', '- uses: softprops/action-gh-release@v2'],
      ['rejects', '- uses: gitleaks/gitleaks-action@v2'],
      ['rejects', "- uses: 'actions/checkout@v4'"],
      // `@v6` moves to the newest v6 release, which is past v6.2.0.
      ['accepts', '- uses: actions/setup-go@v6'],
      ['accepts', '- uses: github/codeql-action/analyze@v4'],
      ['accepts', '- uses: softprops/action-gh-release@v3'],
    ];
    for (const [verdict, step] of cases) {
      test(`${verdict} ${step}`, () => (verdict === 'rejects' ? rejected(step) : accepted(step)));
    }
  });

  describe('judges a commit SHA by its release comment', () => {
    const cases = [
      ['rejects', `- uses: actions/setup-go@${setupGo['v6.1.0']} # v6.1.0`],
      ['rejects', `- uses: actions/setup-go@${setupGo['v6.0.0']} # v6.0.0`],
      // A commit is fixed, so `# v6` could be v6.0.0: read as the lowest release.
      ['rejects', `- uses: actions/setup-go@${setupGo['v6.2.0']} # v6`],
      ['rejects', '- uses: gitleaks/gitleaks-action@ff98106e4c7b2bc287b24eaf42907196329070c7 # v2.3.9'],
      // The known Node 20 commits are caught even when the release comment is gone.
      ['rejects', '- uses: gitleaks/gitleaks-action@ff98106e4c7b2bc287b24eaf42907196329070c7'],
      ['rejects', '- uses: aquasecurity/trivy-action@57a97c7e7821a5776cebc9bb87c984fa69cba8f1'],
      // An unknown commit is judged by its release comment.
      ['rejects', '- uses: aquasecurity/trivy-action@0123456789abcdef0123456789abcdef01234567 # 0.34.2'],
      ['accepts', `- uses: actions/setup-go@${setupGo['v6.2.0']} # v6.2.0`],
      ['accepts', '- uses: actions/checkout@08c6903cd8c0fde910a37f88322edcfb5dd907a8 # v5'],
      ['accepts', '- uses: gitleaks/gitleaks-action@e0c47f4f8be36e29cdc102c57e68cb5cbf0e8d1e # v3.0.0'],
      ['accepts', '- uses: aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25 # v0.36.0'],
    ];
    for (const [verdict, step] of cases) {
      test(`${verdict} ${step}`, () => (verdict === 'rejects' ? rejected(step) : accepted(step)));
    }
  });

  test('ignores comments and actions it has no Node 24 baseline for', () => {
    const { status, output } = gateOnSteps(
      '# - uses: actions/checkout@v4 (a comment, not a step)',
      '- uses: example/unlisted-action@v1',
      '- uses: example/pinned-action@0123456789abcdef0123456789abcdef01234567 # v1.0.0',
    );
    assert.equal(status, 0, output);
  });

  test('still rejects a discarded exit status', () => {
    const { status, output } = gateOnSteps('- run: make lint || true');
    assert.equal(status, 1, output);
    assert.match(output, /discards the command's exit status/);
  });

  test('still rejects a floating action ref', () => {
    const { status, output } = gateOnSteps('- uses: example/action@main');
    assert.equal(status, 1, output);
    assert.match(output, /floating branch/);
  });

  test('fails when the directory holds no workflow files', () => {
    const empty = mkdtempSync(join(scratch, 'empty-'));
    const { status, output } = runGate(empty);
    assert.equal(status, 1, output);
    assert.match(output, /no workflow files to check/);
  });
});
