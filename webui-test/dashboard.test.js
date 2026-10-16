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

// The page kicks off refreshData(), refreshMetrics() and polling intervals at
// script scope. All are stubbed: fetch returns a shaped but empty payload per
// endpoint so the initial refresh is a no-op, and the timers do nothing so the
// process can exit.
const noop = () => 0;
const stubFetch = (url) => {
  let body = null;
  if (url.startsWith('/api/metrics')) body = { summary: {}, requests: [] };
  else if (url === '/api/accounts') body = [];
  else if (url === '/api/models') body = [];
  return Promise.resolve({ ok: true, json: () => Promise.resolve(body) });
};

const runner = new Function('document', 'fetch', 'setInterval', 'setTimeout', appSource + `
  return { providerMeta, accountSubtitle, renderProviderFilter, renderModels, filterModels,
           accountStatusBadge, accountRecoveryNote,
           setModels: (m) => { ALL_MODELS = m; }, getModels: () => ALL_MODELS };
`);
const api = runner(documentStub, stubFetch, noop, noop);

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

console.log('account lifecycle badges');

// Each lifecycle state must be visually distinct: an operator needs to tell at
// a glance whether an account clears in minutes or waits for a daily reset.
const badgeCases = [
  ['healthy', 'badge-green', '就绪'],
  ['cooling', 'badge-amber', '软冷却'],
  ['frozen', 'badge-red', '硬冷却'],
  ['dead', 'badge-red', '已失效'],
  ['expired', 'badge-gray', '令牌过期'],
];
for (const [status, klass, label] of badgeCases) {
  const html = api.accountStatusBadge({ status });
  check(`status ${status} renders ${label}`, html.includes(klass) && html.includes(label), html);
}

check('an unknown status falls back to healthy',
  api.accountStatusBadge({ status: 'something-new' }).includes('badge-green'));

// Soft and hard cooldowns must not look the same.
check('soft and hard cooldowns are visually distinct',
  api.accountStatusBadge({ status: 'cooling' }) !== api.accountStatusBadge({ status: 'frozen' }));

console.log('recovery countdown');

// The server formats timestamps in its own local time (no timezone suffix), so
// the fixture is built the same way rather than from a UTC ISO string.
function localStamp(msFromNow) {
  const d = new Date(Date.now() + msFromNow);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ` +
         `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

const soonStamp = localStamp(30 * 60000);
const note = api.accountRecoveryNote({ status: 'cooling', recover_at: soonStamp });
check('a cooling account shows a countdown', note.includes('分钟'), note);

check('a long wait is shown in hours',
  api.accountRecoveryNote({ status: 'frozen', recover_at: localStamp(5 * 3600000) }).includes('小时'),
  api.accountRecoveryNote({ status: 'frozen', recover_at: localStamp(5 * 3600000) }));

check('a healthy account shows no countdown',
  api.accountRecoveryNote({ status: 'healthy', recover_at: soonStamp }) === '');
check('a missing timestamp shows no countdown',
  api.accountRecoveryNote({ status: 'cooling' }) === '');
check('a past timestamp shows no countdown',
  api.accountRecoveryNote({ status: 'cooling', recover_at: localStamp(-60000) }) === '');

if (failures > 0) {
  console.log(`\n${failures} check(s) failed`);
  process.exit(1);
}
console.log('\nAll dashboard checks passed');
