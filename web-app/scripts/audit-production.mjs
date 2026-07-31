import { readFile } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import process from 'node:process';
import { evaluateAuditReport } from './audit-policy.mjs';

const allowlist = JSON.parse(
  await readFile(new URL('../audit-allowlist.json', import.meta.url), 'utf8'),
);
const audit = spawnSync('npm', ['audit', '--omit=dev', '--json'], {
  encoding: 'utf8',
  shell: process.platform === 'win32',
});

if (audit.error) {
  throw audit.error;
}

let report;
try {
  report = JSON.parse(audit.stdout);
} catch {
  console.error(audit.stderr || audit.stdout);
  throw new Error(`npm audit did not return valid JSON (exit ${audit.status})`);
}

if (audit.status !== 0 && audit.status !== 1) {
  console.error(audit.stderr || audit.stdout);
  throw new Error(`npm audit failed unexpectedly with exit ${audit.status}`);
}

const { failures, usedExceptions } = evaluateAuditReport(report, allowlist);

if (failures.length > 0) {
  console.error('Production dependency audit failed:');
  for (const failure of failures) {
    console.error(`- ${failure}`);
  }
  process.exit(1);
}

if (usedExceptions.size > 0) {
  console.warn(`Accepted ${usedExceptions.size} documented high-severity exception(s); no unexpected high or critical advisories found.`);
} else {
  console.log('No production high or critical advisories found.');
}
