import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import postcss from 'postcss';

const assetsDir = path.resolve('dist/assets');
const assetNames = await readdir(assetsDir);
const cssAssets = assetNames.filter((name) => name.endsWith('.css'));

if (cssAssets.length === 0) {
  throw new Error(`No built CSS assets found in ${assetsDir}`);
}

const selectors = new Set();
for (const assetName of cssAssets) {
  const css = await readFile(path.join(assetsDir, assetName), 'utf8');
  postcss.parse(css).walkRules((rule) => {
    for (const selector of rule.selectors ?? []) {
      selectors.add(selector);
    }
  });
}

const requiredClasses = [
  'inset-0',
  'z-modal',
  'z-modal-backdrop',
  'max-w-2xl',
  'bg-surface',
  'shadow-raised',
  'ease-out-quart',
  'p-4',
];

const missingClasses = requiredClasses.filter(
  (className) => !selectors.has(`.${className}`),
);

if (missingClasses.length > 0) {
  console.error(`Built CSS is missing required selectors: ${missingClasses.join(', ')}`);
  process.exit(1);
}

console.log(`Verified ${requiredClasses.length} required selectors across ${cssAssets.length} CSS asset(s).`);
