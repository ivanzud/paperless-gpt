const blockedSeverities = new Set(['high', 'critical']);

export function evaluateAuditReport(report, allowlist, today = new Date().toISOString().slice(0, 10)) {
  if (allowlist.schemaVersion !== 1 || !Array.isArray(allowlist.exceptions)) {
    throw new Error('Unsupported or malformed audit allowlist');
  }

  const activeExceptions = new Map();
  for (const exception of allowlist.exceptions) {
    if (
      !Number.isInteger(exception.source)
      || typeof exception.package !== 'string'
      || typeof exception.url !== 'string'
      || exception.severity !== 'high'
      || !/^\d{4}-\d{2}-\d{2}$/.test(exception.expires)
      || typeof exception.rationale !== 'string'
      || exception.rationale.trim().length === 0
    ) {
      throw new Error(`Malformed audit exception for ${exception.package ?? 'unknown package'}`);
    }
    if (exception.expires < today) {
      throw new Error(`Expired audit exception for ${exception.package}: ${exception.expires}`);
    }

    const key = `${exception.package}:${exception.source}`;
    if (activeExceptions.has(key)) {
      throw new Error(`Duplicate audit exception: ${key}`);
    }
    activeExceptions.set(key, exception);
  }

  const vulnerabilities = report.vulnerabilities ?? {};
  const usedExceptions = new Set();
  const failures = [];
  const checked = new Set();
  const checking = new Set();

  function checkVulnerability(packageName) {
    if (checked.has(packageName)) {
      return;
    }
    if (checking.has(packageName)) {
      failures.push(`${packageName}: cyclic npm audit dependency chain`);
      return;
    }

    const vulnerability = vulnerabilities[packageName];
    if (!vulnerability || !blockedSeverities.has(vulnerability.severity)) {
      return;
    }
    if (vulnerability.severity === 'critical') {
      failures.push(`${packageName}: critical vulnerabilities cannot be excepted`);
      checked.add(packageName);
      return;
    }

    const viaEntries = vulnerability.via ?? [];
    if (viaEntries.length === 0) {
      failures.push(`${packageName}: high vulnerability has no reviewable advisory chain`);
      checked.add(packageName);
      return;
    }

    checking.add(packageName);
    for (const via of viaEntries) {
      if (typeof via === 'string') {
        const referenced = vulnerabilities[via];
        if (!referenced || !blockedSeverities.has(referenced.severity)) {
          failures.push(`${packageName}: unresolved high-severity advisory chain via ${via}`);
        } else {
          checkVulnerability(via);
        }
        continue;
      }

      const key = `${packageName}:${via.source}`;
      const exception = activeExceptions.get(key);
      if (
        !exception
        || exception.url !== via.url
        || exception.severity !== via.severity
      ) {
        failures.push(`${packageName}: ${via.title} (${via.url})`);
        continue;
      }
      usedExceptions.add(key);
    }
    checking.delete(packageName);
    checked.add(packageName);
  }

  for (const packageName of Object.keys(vulnerabilities)) {
    checkVulnerability(packageName);
  }

  for (const key of activeExceptions.keys()) {
    if (!usedExceptions.has(key)) {
      failures.push(`Unused audit exception must be removed or updated: ${key}`);
    }
  }

  return { failures, usedExceptions };
}
