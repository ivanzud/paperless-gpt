import assert from 'node:assert/strict';
import test from 'node:test';
import { evaluateAuditReport } from './audit-policy.mjs';

const allowedAdvisory = {
  source: 1124282,
  package: 'react-router',
  url: 'https://github.com/advisories/GHSA-qwww-vcr4-c8h2',
  severity: 'high',
  expires: '2026-10-31',
  rationale: 'Only affects server-side React Server Components, which this static SPA does not use.',
};

function reportWith(advisory = allowedAdvisory) {
  return {
    vulnerabilities: {
      'react-router': {
        severity: advisory.severity,
        via: [{
          source: advisory.source,
          title: 'RSC CSRF',
          url: advisory.url,
          severity: advisory.severity,
        }],
      },
      'react-router-dom': {
        severity: advisory.severity,
        via: ['react-router'],
      },
    },
  };
}

test('accepts only the exact documented high advisory and its dependency wrapper', () => {
  const result = evaluateAuditReport(
    reportWith(),
    { schemaVersion: 1, exceptions: [allowedAdvisory] },
    '2026-07-31',
  );
  assert.deepEqual(result.failures, []);
  assert.deepEqual([...result.usedExceptions], ['react-router:1124282']);
});

test('rejects an unexpected high advisory', () => {
  const unexpected = { ...allowedAdvisory, source: 9999999 };
  const result = evaluateAuditReport(
    reportWith(unexpected),
    { schemaVersion: 1, exceptions: [allowedAdvisory] },
    '2026-07-31',
  );
  assert.ok(result.failures.some((failure) => failure.includes('RSC CSRF')));
});

test('rejects critical advisories even when they resemble an exception', () => {
  const critical = { ...allowedAdvisory, severity: 'critical' };
  const result = evaluateAuditReport(
    reportWith(critical),
    { schemaVersion: 1, exceptions: [allowedAdvisory] },
    '2026-07-31',
  );
  assert.ok(result.failures.some((failure) => failure.includes('cannot be excepted')));
});

test('rejects expired and stale exceptions', () => {
  assert.throws(
    () => evaluateAuditReport(
      reportWith(),
      { schemaVersion: 1, exceptions: [{ ...allowedAdvisory, expires: '2026-07-30' }] },
      '2026-07-31',
    ),
    /Expired audit exception/,
  );

  const result = evaluateAuditReport(
    { vulnerabilities: {} },
    { schemaVersion: 1, exceptions: [allowedAdvisory] },
    '2026-07-31',
  );
  assert.ok(result.failures.some((failure) => failure.includes('Unused audit exception')));
});
