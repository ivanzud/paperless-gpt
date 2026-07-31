import assert from 'node:assert/strict';
import test from 'node:test';
import { inspectPaperlessTaskPayload } from '../e2e/paperless-task.mjs';

test('parses Paperless 3 paginated task responses', () => {
  assert.deepEqual(
    inspectPaperlessTaskPayload({
      count: 1,
      results: [{
        id: 99,
        status: 'success',
        result_data: { document_id: 42 },
        related_document_ids: [42],
      }],
    }),
    { state: 'success', documentId: 42 },
  );
});

test('parses legacy task responses case-insensitively', () => {
  assert.deepEqual(
    inspectPaperlessTaskPayload([{ status: 'SUCCESS', id: 17 }]),
    { state: 'success', documentId: 17 },
  );
});

test('extracts document IDs from supported fallback fields', () => {
  const cases = [
    [{ status: 'success', result: { document_id: 21 } }, 21],
    [{ status: 'success', result: '22' }, 22],
    [{ status: 'success', document_id: 23 }, 23],
    [{ status: 'success', related_document_ids: [24] }, 24],
  ];

  for (const [task, expectedId] of cases) {
    assert.deepEqual(
      inspectPaperlessTaskPayload({ results: [task] }),
      { state: 'success', documentId: expectedId },
    );
  }
});

test('treats empty and non-terminal responses as pending', () => {
  assert.deepEqual(inspectPaperlessTaskPayload({ results: [] }), { state: 'pending' });
  assert.deepEqual(
    inspectPaperlessTaskPayload({ results: [{ status: 'started' }] }),
    { state: 'pending' },
  );
});

test('rejects failed tasks and successful tasks without a document ID', () => {
  assert.throws(
    () => inspectPaperlessTaskPayload({ results: [{ status: 'failed', result: 'bad input' }] }),
    /Document processing failed/,
  );
  assert.throws(
    () => inspectPaperlessTaskPayload({ results: [{ status: 'success', id: 99 }] }),
    /succeeded without a document ID/,
  );
});
