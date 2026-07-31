function asDocumentId(value) {
  const id = typeof value === 'string' ? Number(value) : value;
  return typeof id === 'number' && Number.isInteger(id) && id > 0 ? id : undefined;
}

export function inspectPaperlessTaskPayload(payload) {
  const isLegacyResponse = Array.isArray(payload);
  const taskResults = isLegacyResponse
    ? payload
    : (payload && typeof payload === 'object' && Array.isArray(payload.results)
        ? payload.results
        : []);
  const task = taskResults[0];

  if (!task || typeof task !== 'object') {
    return { state: 'pending' };
  }

  const status = typeof task.status === 'string' ? task.status.toLowerCase() : '';
  if (status === 'failed' || status === 'failure') {
    throw new Error(`Document processing failed: ${JSON.stringify(task.result)}`);
  }

  if (status !== 'success') {
    return { state: 'pending' };
  }

  const resultDocumentId =
    task.result && typeof task.result === 'object'
      ? task.result.document_id
      : task.result;
  const documentId =
    asDocumentId(task.result_data?.document_id) ??
    asDocumentId(resultDocumentId) ??
    asDocumentId(task.document_id) ??
    asDocumentId(task.related_document_ids?.[0]) ??
    (isLegacyResponse ? asDocumentId(task.id) : undefined);

  if (!documentId) {
    throw new Error(`Document task succeeded without a document ID: ${JSON.stringify(task)}`);
  }

  return { state: 'success', documentId };
}
