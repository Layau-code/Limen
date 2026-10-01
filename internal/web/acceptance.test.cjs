const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

// Test the production mapping/rendering functions without a browser or network.
function loadUI() {
  const context = vm.createContext({
    window: { LimenFixtures: { overview: { updated: 'test' }, decisions: {} } },
    sessionStorage: { getItem: () => '' },
    document: { getElementById: () => ({ addEventListener() {} }) },
    location: { hash: '#/decisions/live-decision' },
  });
  const source = fs.readFileSync(path.join(__dirname, 'assets/app.js'), 'utf8');
  const bootstrap = '  window.addEventListener("hashchange", render);\n  render();\n  refreshLive();';
  assert.ok(source.includes(bootstrap));
  vm.runInContext(source.replace(bootstrap,
    '  globalThis.ui = { state, mapLiveDecision, renderDecision };'), context);
  return context.ui;
}

function renderLive(targets) {
  const ui = loadUI();
  const raw = {
    decision_id: 'live-decision',
    input: { request: { model: 'auto', contract: { data_class: 'public' } } },
    plan: {
      targets: targets.map(target_id => ({ target_id })),
      candidates: targets.map(target_id => ({ target_id, accepted: true, reason: 'eligible' })),
      semantic_status: 'not_evaluated',
    },
  };
  ui.state.live = true;
  ui.state.liveDecisions[raw.decision_id] = ui.mapLiveDecision(raw, null);
  return ui.renderDecision(raw.decision_id, false);
}

test('live decision never presents fixture execution or costs as evidence', () => {
  const html = renderLive(['opaque-primary', 'opaque-fallback']);
  assert.match(html, /Not available/);
  assert.match(html, /Missing execution data does not mean zero cost/);
  assert.match(html, /Jev assessment cost is measured separately/);
  assert.doesNotMatch(html, /2 attempts|settlement complete|1\.2s|\$0\.097/);
  assert.equal((html.match(/>SELECTED</g) || []).length, 1);
  assert.equal((html.match(/>FALLBACK</g) || []).length, 1);
});

test('empty live plans are shown as rejected', () => {
  const html = renderLive([]);
  assert.match(html, /No eligible target/);
  assert.doesNotMatch(html, /Decision accepted/);
});
