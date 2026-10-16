// Tests for the embedded dashboard's model rendering.
//
// The dashboard is hand-written inline JavaScript with no build step, so a
// typo or a stale provider assumption is invisible to `go test`. This extracts
// the app script from the served HTML and runs its render path against a
// minimal DOM stub, which catches both syntax errors and wrong output.
//
// Run with: node webui-test/dashboard.test.js

'use strict';

const fs = require('fs');
const path = require('path');

const HTML_PATH = path.join(__dirname, '..', 'internal', 'web', 'static', 'index.html');
const html = fs.readFileSync(HTML_PATH, 'utf8');

// --- extract the application script ----------------------------------------

const blocks = [...html.matchAll(/<script[^>]*>([\s\S]*?)<\/script>/g)].map(m => m[1]);
if (blocks.length === 0) {
  console.error('FAIL: no <script> block found in index.html');
  process.exit(1);
}
const appBlocks = blocks.filter(b => b.includes('function refreshData'));
if (appBlocks.length !== 1) {
  console.error(`FAIL: expected exactly one app script block, found ${appBlocks.length}`);
  process.exit(1);
}
const appSource = appBlocks[0];

// --- minimal DOM stub -------------------------------------------------------

const elements = {};
function makeElement(id) {
  return {
    id,
    innerHTML: '',
    innerText: '',
    value: id === 'model-filter' ? 'all' : '',
    scrollIntoView() {},
  };
}

const documentStub = {
  getElementById(id) {
    if (!elements[id]) elements[id] = makeElement(id);
    return elements[id];
  },
};

// --- load the app script ----------------------------------------------------

// The page kicks off refreshData() and a polling interval at script scope.
// Both are stubbed: fetch resolves to nothing so the initial refresh is a
// no-op, and setInterval is a no-op so the process can exit.
const noop = () => 0;
const failingFetch = () => Promise.resolve({ json: () => Promise.resolve(null) });

const runner = new Function('document', 'fetch', 'setInterval', 'setTimeout', appSource + `
  return { providerMeta, accountSubtitle, renderProviderFilter, renderModels, filterModels,
           setModels: (m) => { ALL_MODELS = m; }, getModels: () => ALL_MODELS };
`);
const api = runner(documentStub, failingFetch, noop, noop);

// --- assertions -------------------------------------------------------------

let failures = 0;
function check(name, condition, detail) {
  if (condition) {
    console.log('  PASS  ' + name);
  } else {
    console.log('  FAIL  ' + name + (detail ? ' -- ' + detail : ''));
    failures++;
  }
}

console.log('provider labels');

check('workbuddy label',
  api.providerMeta('workbuddy').label === 'Tencent WorkBuddy');
check('antigravity label',
  api.providerMeta('antigravity').label === 'Google Antigravity');
check('qoder label is Qoder, not WorkBuddy',
  api.providerMeta('qoder').label === 'Alibaba Qoder',
  'got ' + api.providerMeta('qoder').label);
check('an unknown provider is labelled with its own id',
  api.providerMeta('somefutureprovider').label === 'somefutureprovider',
  'got ' + api.providerMeta('somefutureprovider').label);
check('an unknown provider is not mislabelled as WorkBuddy',
  api.providerMeta('somefutureprovider').label !== 'WorkBuddy');

console.log('account subtitles');

check('antigravity shows its project',
  api.accountSubtitle({ type: 'antigravity', project_id: 'proj-1' }) === 'proj-1');
check('workbuddy cn shows the domestic region',
  api.accountSubtitle({ type: 'workbuddy', realm: 'cn' }) === '国内版');
check('qoder does not inherit the workbuddy subtitle',
  api.accountSubtitle({ type: 'qoder', realm: 'intl' }) === '国际版');

console.log('provider filter is built from the data');

api.setModels([
  { id: 'a', provider: 'workbuddy', name: 'A', supports_tool_call: true },
  { id: 'b', provider: 'qoder', name: 'B' },
  { id: 'c', provider: 'qoder', name: 'C' },
  { id: 'd', provider: 'antigravity', name: 'D' },
]);
api.renderProviderFilter();
const options = elements['model-filter'].innerHTML;

check('the filter offers Qoder', options.includes('qoder'), options);
check('the filter offers all three providers',
  options.includes('workbuddy') && options.includes('antigravity') && options.includes('qoder'));
check('the filter shows per-provider counts', options.includes('Qoder (2)'), options);
check('the filter keeps an "all" option', options.includes('value="all"'));

console.log('model cards');

api.renderModels([
  { id: 'qmodel', provider: 'qoder', name: 'Qwen' },
  { id: 'gem', provider: 'antigravity', name: 'Gemini' },
  { id: 'wb', provider: 'workbuddy', name: 'Deepseek' },
]);
const cards = elements['models-container'].innerHTML;

check('a Qoder card is labelled Qoder', cards.includes('Alibaba Qoder'));
check('a Qoder card is not labelled WorkBuddy',
  !/qmodel[\s\S]{0,400}?Tencent WorkBuddy/.test(cards),
  'the qoder card carries a WorkBuddy badge');
check('rendering a mix of providers does not throw', cards.length > 0);

console.log('filtering by provider');

api.setModels([
  { id: 'qmodel', provider: 'qoder', name: 'Qwen' },
  { id: 'wb', provider: 'workbuddy', name: 'Deepseek' },
]);
api.renderProviderFilter();
// Touch the search box so the stub creates it before filterModels reads it.
documentStub.getElementById('model-search').value = '';
documentStub.getElementById('model-filter').value = 'qoder';
api.filterModels();

// filterModels writes into models-container; a full-model render must not leak.
const filtered = elements['models-container'].innerHTML;
check('filtering to qoder excludes workbuddy models',
  filtered.includes('qmodel') && !filtered.includes('>wb<'),
  'filter leaked a workbuddy model');

if (failures > 0) {
  console.log(`\n${failures} check(s) failed`);
  process.exit(1);
}
console.log('\nAll dashboard checks passed');
