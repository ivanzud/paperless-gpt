import { expect, Locator, Page, test, TestInfo } from '@playwright/test';
import { mkdir } from 'fs/promises';
import path, { dirname } from 'path';
import { fileURLToPath } from 'url';
import { addTagToDocument, PORTS, setupTestEnvironment, TestEnvironment, uploadDocument } from './test-environment';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
let testEnv: TestEnvironment;

async function capture(page: Page, testInfo: TestInfo, name: string) {
  const directory = path.resolve('test-results', 'screenshots', testInfo.project.name);
  await mkdir(directory, { recursive: true });
  await page.screenshot({
    path: path.join(directory, `${name}.png`),
    fullPage: true,
  });
}

async function expectNoHorizontalOverflow(page: Page) {
  const dimensions = await page.evaluate(() => ({
    viewportWidth: window.innerWidth,
    documentWidth: document.documentElement.scrollWidth,
  }));
  expect(dimensions.documentWidth).toBeLessThanOrEqual(dimensions.viewportWidth + 1);
}

async function expectWithinViewport(page: Page, locator: Locator) {
  await expect(locator).toBeVisible();
  const [box, viewport] = await Promise.all([
    locator.boundingBox(),
    page.evaluate(() => ({ width: window.innerWidth, height: window.innerHeight })),
  ]);
  expect(box).not.toBeNull();
  expect(box!.x).toBeGreaterThanOrEqual(0);
  expect(box!.y).toBeGreaterThanOrEqual(0);
  expect(box!.x + box!.width).toBeLessThanOrEqual(viewport.width + 1);
  expect(box!.y + box!.height).toBeLessThanOrEqual(viewport.height + 1);
}

async function expectNoOverlap(first: Locator, second: Locator) {
  const [firstBox, secondBox] = await Promise.all([
    first.boundingBox(),
    second.boundingBox(),
  ]);
  expect(firstBox).not.toBeNull();
  expect(secondBox).not.toBeNull();

  const separated =
    firstBox!.x + firstBox!.width <= secondBox!.x ||
    secondBox!.x + secondBox!.width <= firstBox!.x ||
    firstBox!.y + firstBox!.height <= secondBox!.y ||
    secondBox!.y + secondBox!.height <= firstBox!.y;
  expect(separated).toBe(true);
}

async function expectHistoryCardsFit(page: Page) {
  const cards = page.locator('.undo-card:visible');
  const count = await cards.count();
  expect(count).toBeGreaterThan(0);

  for (let index = 0; index < count; index += 1) {
    const card = cards.nth(index);
    await card.evaluate((element) =>
      element.scrollIntoView({ block: 'center', inline: 'nearest' })
    );

    const content = card.locator('.undo-card__content');
    const metadata = card.locator('.undo-card__metadata');
    const values = card.locator('.undo-card__values');
    const action = card.locator('.undo-card__action');
    const button = action.getByRole('button');

    await expectWithinViewport(page, card);
    await expectWithinViewport(page, content);
    await expectWithinViewport(page, metadata);
    await expectWithinViewport(page, values);
    await expectWithinViewport(page, action);
    await expectWithinViewport(page, button);
    await expectNoOverlap(content, action);
    await expectNoHorizontalOverflow(page);
  }
}

async function fieldChangeCount(dialog: Locator): Promise<number> {
  const summary = dialog.getByText(/field changes? will be written/);
  await expect(summary).toBeVisible();
  const text = await summary.textContent();
  const match = text?.match(/(\d+) field changes?/);
  expect(match).not.toBeNull();
  return Number(match![1]);
}

test.beforeAll(async () => {
  testEnv = await setupTestEnvironment();
});

test.afterAll(async () => {
  await testEnv?.cleanup();
});

test.beforeEach(async ({ page }, testInfo) => {
  await page.goto(`http://localhost:${testEnv.paperlessGpt.getMappedPort(PORTS.paperlessGpt)}`);
  await expectNoHorizontalOverflow(page);
  await capture(page, testInfo, 'initial-state');
});

test('should process document and show changes in history', async ({ page }, testInfo) => {
  const paperlessNgxPort = testEnv.paperlessNgx.getMappedPort(PORTS.paperlessNgx);
  const paperlessGptPort = testEnv.paperlessGpt.getMappedPort(PORTS.paperlessGpt);
  const credentials = { username: 'admin', password: 'admin' };

  // 1. Upload document and add initial tag via API
  const baseUrl = `http://localhost:${paperlessNgxPort}`;
  const documentPath = path.join(__dirname, 'fixtures', 'test-document.txt');
  
  // Get the paperless-gpt tag ID
  const response = await fetch(`${baseUrl}/api/tags/?name=paperless-gpt`, {
    headers: {
      'Authorization': 'Basic ' + btoa(`${credentials.username}:${credentials.password}`),
    },
  });

  if (!response.ok) {
    throw new Error('Failed to fetch paperless-gpt tag');
  }

  const tags = await response.json();
  if (!tags.results || tags.results.length === 0) {
    throw new Error('paperless-gpt tag not found');
  }

  const tagId = tags.results[0].id;

  // Upload document and get ID
  const { id: documentId } = await uploadDocument(
    baseUrl,
    documentPath,
    'Original Title',
    credentials
  );

  console.log(`Document ID: ${documentId}`);

  // Add tag to document
  await addTagToDocument(
    baseUrl,
    documentId,
    tagId,
    credentials
  );

  // 2. Navigate to Paperless-GPT UI and process the document
  await page.goto(`http://localhost:${paperlessGptPort}`);
  
  // Wait for document to appear in the list
  await page.waitForSelector('.document-card', { timeout: 1000 * 60 });
  await expectNoHorizontalOverflow(page);
  await capture(page, testInfo, 'document-loaded');
  
  // Click the process button (starts an async suggestion job with progress UI)
  const summaryGenerationToggle = page.getByRole('checkbox', { name: 'Summary' });
  await expect(summaryGenerationToggle).toBeChecked();
  const generationRequestPromise = page.waitForRequest(
    (request) =>
      request.method() === 'POST' &&
      request.url().endsWith('/api/jobs/suggestions')
  );
  await page.click('button:has-text("Generate suggestions")');
  const generationRequest = await generationRequestPromise;
  const generationPayload = generationRequest.postDataJSON();
  expect(generationPayload.generate_summary).toBe(true);
  expect(generationPayload.documents[0].original_file_name).toBeTruthy();

  // Wait for the job to finish and the review to appear
  await page.waitForSelector('.suggestions-review', { timeout: 60000 });
  await expectNoHorizontalOverflow(page);
  await capture(page, testInfo, 'suggestions-loaded');

  // Summary is generated, editable, and participates in field-level inclusion.
  const summaryInput = page.getByRole('textbox', { name: 'Suggested summary' }).first();
  await expect(summaryInput).toHaveValue('Mock generated summary for frontend review.');
  const editedSummary = 'Reviewed summary applied by the frontend E2E flow.';
  await page
    .getByRole('button', { name: /Open “Original Title” in focus view/ })
    .click();
  const focusDialog = page.getByRole('dialog');
  const focusSummaryInput = focusDialog.getByRole('textbox', {
    name: 'Suggested summary',
  });
  await expect(focusSummaryInput).toHaveValue(
    'Mock generated summary for frontend review.'
  );
  await focusSummaryInput.fill(editedSummary);
  await capture(page, testInfo, 'summary-edited-in-focus');
  await focusDialog
    .getByRole('button', { name: 'Close focus view' })
    .click();
  await expect(summaryInput).toHaveValue(editedSummary);

  const applySummaryToggle = page
    .getByRole('checkbox', { name: 'Apply suggested summary' })
    .first();
  await expect(applySummaryToggle).toBeChecked();
  await applySummaryToggle.uncheck();

  await page.getByRole('button', { name: /Apply remaining/ }).click();
  let dialog = page.getByRole('dialog');
  const excludedCount = await fieldChangeCount(dialog);
  await expect(dialog).not.toContainText('Summary');
  await dialog.getByRole('button', { name: 'Cancel' }).click();

  await applySummaryToggle.check();

  // Apply all remaining suggestions via the summary dialog
  await page.getByRole('button', { name: /Apply remaining/ }).click();
  dialog = page.getByRole('dialog');
  const includedCount = await fieldChangeCount(dialog);
  expect(includedCount).toBe(excludedCount + 1);
  await expect(dialog).toContainText('Summary');
  const dialogPanel = dialog.locator('.modal-panel');
  const confirmButton = page.getByRole('button', { name: /^Apply to \d+ documents?$/ });
  await expectWithinViewport(page, dialogPanel);
  await expectWithinViewport(page, confirmButton);
  await capture(page, testInfo, 'apply-confirmation');
  const updateRequestPromise = page.waitForRequest(
    (request) =>
      request.method() === 'PATCH' &&
      request.url().endsWith('/api/update-documents')
  );
  await confirmButton.click();
  const updateRequest = await updateRequestPromise;
  const updatePayload = updateRequest.postDataJSON();
  expect(updatePayload[0].suggested_summary).toBe(editedSummary);

  // Wait for the success toast (review closes automatically once all documents are decided)
  await page.waitForSelector('div[role="status"]:has-text("Applied")', { timeout: 15000 });
  await capture(page, testInfo, 'suggestions-applied');

  // 3. Check history page for the modifications
  await page.getByRole('link', { name: 'History', exact: true }).click();
  
  // Wait for history page to load
  await page.waitForSelector('.modification-history', { timeout: 5000 });
  await expectNoHorizontalOverflow(page);
  await capture(page, testInfo, 'history-page');

  // Verify at least one modification entry exists
  const modifications = await page.locator('.undo-card').count();
  expect(modifications).toBeGreaterThan(0);
  await expectHistoryCardsFit(page);
  await capture(page, testInfo, 'history-cards-verified');

  // Undo the created Paperless note through the Summary history entry.
  const summaryModification = page
    .locator('.undo-card')
    .filter({ has: page.getByText('summary', { exact: true }) })
    .first();
  await expect(summaryModification).toBeVisible();
  const summaryUndoButton = summaryModification.locator(
    '.undo-card__action button'
  );
  await expect(summaryUndoButton).toBeEnabled();
  await summaryUndoButton.click();
  await expect(summaryUndoButton).toContainText('Undone');
  await expectHistoryCardsFit(page);
  await capture(page, testInfo, 'summary-history-undone-verified');

  // Verify modification details
  const firstModification = await page.locator('.undo-card:has-text("Original Title")').first();
  
  // Check if title was modified
  const titleChange = await firstModification.isVisible();
  expect(titleChange).toBeTruthy();

  // Test pagination if there are multiple modifications
  const paginationVisible = await page.locator('.pagination-controls').isVisible();
  if (paginationVisible) {
    // Click next page if available
    const nextButton = page.locator('button:has-text("Next")');
    if (await nextButton.isEnabled()) {
      await nextButton.click();
      // Wait for new items to load
      await page.waitForSelector('.undo-card');
    }
  }

  // 4. Test undo functionality
  const undoButton = await firstModification.locator('button:has-text("Undo")');
  if (await undoButton.isEnabled()) {
    await undoButton.click();
    // Wait for undo to complete. Text should change to "Undone"
    await expect(firstModification.locator('.undo-card__action button')).toContainText('Undone');
    await expectHistoryCardsFit(page);
    await capture(page, testInfo, 'history-undone-verified');
  }
});
