import { chromium } from '@playwright/test';
import * as nodeFetch from 'node-fetch';

// Polyfill fetch for Node.js environment
if (!globalThis.fetch) {
  globalThis.fetch = nodeFetch.default as typeof fetch;
  globalThis.Headers = nodeFetch.Headers as typeof Headers;
  globalThis.Request = nodeFetch.Request as typeof Request;
  globalThis.Response = nodeFetch.Response as typeof Response;
  globalThis.FormData = nodeFetch.FormData as typeof FormData;
}

async function globalSetup() {
  // Install Playwright browser if needed
  const browser = await chromium.launch();
  await browser.close();

  // Load environment variables
  if (!process.env.OPENAI_API_KEY) {
    console.warn('Warning: OPENAI_API_KEY environment variable is not set');
  }
}

export default globalSetup;
