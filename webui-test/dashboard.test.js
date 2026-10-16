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
    placeholder: '',
    value: id === 'model-filter' ? 'all' : (id === 'import-type' ? 'workbuddy' : ''),
    // Elements carry a style object so visibility toggling can be observed.
    style: {},
    classList: { add() {}, remove() {} },
    appendChild() {},
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
  else if (url.startsWith('/api/checkin')) body = { last_run: {}, providers: [] };
  else if (url === '/api/accounts') body = [];
  else if (url === '/api/models') body = [];
  return Promise.resolve({ ok: true, json: () => Promise.resolve(body) });
};

const runner = new Function('document', 'fetch', 'setInterval', 'setTimeout', 'alert', appSource + `
  return { providerMeta, accountSubtitle, renderProviderFilter, renderModels, filterModels,
           accountStatusBadge, accountRecoveryNote, onImportTypeChange, doImport, openImportModal,
           checkinBadge, renderCheckin,
           setModels: (m) => { ALL_MODELS = m; }, getModels: () => ALL_MODELS };
`);
const api = runner(documentStub, stubFetch, noop, noop, noop);

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

console.log('check-in rendering');

// The four outcomes must be distinguishable: only "failed" is actionable, and
// "already" means nothing went wrong.
for (const [status, klass, label] of [
  ['ok', 'badge-green', '签到成功'],
  ['already', 'badge-blue', '今日已签'],
  ['unsupported', 'badge-gray', '不支持'],
  ['failed', 'badge-red', '失败'],
]) {
  const html = api.checkinBadge(status);
  check(`check-in status ${status} renders ${label}`, html.includes(klass) && html.includes(label), html);
}
check('an unknown check-in status is treated as a failure',
  api.checkinBadge('something-new').includes('badge-red'));

// A streak is progress worth showing.
api.renderCheckin({
  attempted: 1, succeeded: 1, already_done: 0, unsupported: 0, failed: 0,
  results: [{
    provider: 'qoder', account_id: 'a1', account_name: 'acct-a',
    status: 'ok', streak_days: 7, rewards: ['10 credits'],
  }],
});
const cbox = documentStub.getElementById('checkin-container').innerHTML;
check('the streak is shown', cbox.includes('连签 7 天'), cbox);
check('the rewards are shown', cbox.includes('10 credits'), cbox);
check('the account name is shown', cbox.includes('acct-a'), cbox);
check('a success is not styled as a failure', !cbox.includes('badge-red'), cbox);
check('the summary counts are shown',
  documentStub.getElementById('checkin-summary').innerText.includes('成功 1'),
  documentStub.getElementById('checkin-summary').innerText);

// No reported streak must not render "0 天", which would look like a real value.
api.renderCheckin({
  attempted: 1, succeeded: 1,
  results: [{ provider: 'workbuddy', account_name: 'acct-b', status: 'ok' }],
});
check('a missing streak shows nothing',
  !documentStub.getElementById('checkin-container').innerHTML.includes('连签'),
  documentStub.getElementById('checkin-container').innerHTML);

// An empty run must say so rather than render an empty grid.
api.renderCheckin({ attempted: 0, results: [] });
check('an empty run says there is nothing to sign',
  documentStub.getElementById('checkin-container').innerHTML.includes('没有可签到的账号'),
  documentStub.getElementById('checkin-container').innerHTML);
check('an empty run reports "尚未运行"',
  documentStub.getElementById('checkin-summary').innerText === '尚未运行',
  documentStub.getElementById('checkin-summary').innerText);

console.log('import modal');

// Qoder must be offered: its absence is what prompted this, and the backend
// already supports it.
const importTypeHtml = html.match(/<select id="import-type"[\s\S]*?<\/select>/);
check('the import type selector exists', !!importTypeHtml);
check('Qoder is offered as an import type',
  !!importTypeHtml && importTypeHtml[0].includes('value="qoder"'),
  importTypeHtml ? importTypeHtml[0] : 'selector not found');
for (const [type, label] of [['workbuddy', 'WorkBuddy'], ['antigravity', 'Antigravity'], ['qoder', 'Qoder']]) {
  check(`the import selector offers ${label}`,
    !!importTypeHtml && importTypeHtml[0].includes(`value="${type}"`));
}

// The realm row is only meaningful for Qoder, and must be hidden otherwise.
const realmRow = documentStub.getElementById('import-realm-row');
documentStub.getElementById('import-type').value = 'workbuddy';
api.onImportTypeChange();
check('the realm row is hidden for WorkBuddy', realmRow.style.display === 'none');

documentStub.getElementById('import-type').value = 'antigravity';
api.onImportTypeChange();
check('the realm row is hidden for Antigravity', realmRow.style.display === 'none');

documentStub.getElementById('import-type').value = 'qoder';
api.onImportTypeChange();
check('the realm row is shown for Qoder', realmRow.style.display === 'block');

// The placeholder and hint must follow the selected provider, or an operator
// pastes the wrong shape.
const ta = documentStub.getElementById('import-json');
const hint = documentStub.getElementById('import-hint');
check('the placeholder mentions pt- for Qoder', ta.placeholder.includes('pt-'), ta.placeholder);
check('the hint explains the pt- exchange',
  hint.innerText.includes('pt-') && hint.innerText.includes('jt-'), hint.innerText);

documentStub.getElementById('import-type').value = 'antigravity';
api.onImportTypeChange();
check('the placeholder switches for Antigravity', ta.placeholder.includes('ya29'), ta.placeholder);
check('the hint warns about the OAuth client credentials',
  hint.innerText.includes('client_id'), hint.innerText);

console.log('import submits the selected realm');

// A Qoder credential without a realm must pick up the selector's value,
// otherwise it silently targets the wrong host.
let submitted = null;
const capturingFetch = (url, opts) => {
  if (url === '/api/accounts/import' && opts && opts.body) {
    submitted = JSON.parse(opts.body);
    return Promise.resolve({ ok: true, json: () => Promise.resolve({ status: 'ok', models_count: 3, realm: 'cn' }) });
  }
  // Other endpoints keep their expected shapes so a stray refresh does not
  // throw and muddy the output.
  let body = null;
  if (url.startsWith('/api/metrics')) body = { summary: {}, requests: [] };
  else if (url.startsWith('/api/checkin')) body = { last_run: {}, providers: [] };
  else if (url === '/api/accounts' || url === '/api/models') body = [];
  return Promise.resolve({ ok: true, json: () => Promise.resolve(body) });
};

async function runImportChecks() {
  const importApi = new Function('document', 'fetch', 'setInterval', 'setTimeout', 'alert', appSource + `
    return { doImport, onImportTypeChange };
  `)(documentStub, capturingFetch, noop, noop, noop);

  documentStub.getElementById('import-type').value = 'qoder';
  documentStub.getElementById('import-realm').value = 'cn';
  documentStub.getElementById('import-json').value = '{"access_token":"pt-abc","account_name":"u@example.com"}';
  importApi.onImportTypeChange();

  await importApi.doImport();
  await new Promise(r => setTimeout(r, 0));

  check('the import was submitted', !!submitted);
  if (submitted) {
    check('type came from the selector', submitted.type === 'qoder', JSON.stringify(submitted));
    check('realm came from the realm selector', submitted.realm === 'cn', JSON.stringify(submitted));
    check('the token was preserved', submitted.access_token === 'pt-abc', JSON.stringify(submitted));
  }

  // An explicit realm in the pasted JSON wins over the selector.
  submitted = null;
  documentStub.getElementById('import-realm').value = 'cn';
  documentStub.getElementById('import-json').value = '{"access_token":"pt-x","realm":"intl"}';
  await importApi.doImport();
  await new Promise(r => setTimeout(r, 0));
  check('an explicit realm in the JSON is not overwritten',
    submitted && submitted.realm === 'intl', JSON.stringify(submitted));
}

runImportChecks().then(() => {
  if (failures > 0) {
    console.log(`\n${failures} check(s) failed`);
    process.exit(1);
  }
  console.log('\nAll dashboard checks passed');
});
