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
  await page.click('button:has-text("Generate suggestions")');

  // Wait for the job to finish and the review to appear
  await page.waitForSelector('.suggestions-review', { timeout: 60000 });
  await expectNoHorizontalOverflow(page);
  await capture(page, testInfo, 'suggestions-loaded');

  // Apply all remaining suggestions via the summary dialog
  await page.click('button:has-text("Apply remaining")');
  const dialog = page
    .getByRole('heading', { name: /^Apply suggestions to \d+ documents?\?$/ })
    .locator('..');
  const confirmButton = page.getByRole('button', { name: /^Apply to \d+ documents?$/ });
  await expectWithinViewport(page, dialog);
  await expectWithinViewport(page, confirmButton);
  await capture(page, testInfo, 'apply-confirmation');
  await confirmButton.click();

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
    await page.waitForSelector('text=Undone');
  }
});
