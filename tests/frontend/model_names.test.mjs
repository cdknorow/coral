// A model id as people say it (#4589). node model_names.test.mjs
import assert from 'node:assert/strict';
import { modelDisplayName as d } from '../../coral-go/pkg/frontend/static/model_names.js';

const cases = {
    'claude-opus-5-5': 'Opus 5.5', 'claude-sonnet-5-5': 'Sonnet 5.5', 'claude-haiku-5-5': 'Haiku 5.5', 'claude-fable-5-1': 'Fable 5.1',
    'claude-sonnet-4-5-20250929': 'Sonnet 4.5', 'claude-3-5-haiku-20241022': 'Haiku 3.5', 'claude-opus-5': 'Opus 5',
    'claude-opus-5-5[1m]': 'Opus 5.5 (1M)', 'CLAUDE-OPUS-5-5': 'Opus 5.5',
    'gpt-5': 'GPT-5', 'gpt-5-codex': 'GPT-5 Codex', 'gpt-5.1-codex-max': 'GPT-5.1 Codex Max',
    'gemini-2.5-pro': 'Gemini 2.5 Pro', 'gemini-3-flash': 'Gemini 3 Flash',
    // not known here: shown as it is, never guessed at
    'o3-mini': 'o3-mini', 'mystery-model-7': 'mystery-model-7', 'claude-unknown-9': 'claude-unknown-9',
    '': '', '   ': '',
};
for (const [id, want] of Object.entries(cases)) assert.equal(d(id), want, JSON.stringify(id));
assert.equal(d(undefined), ''); assert.equal(d(null), ''); assert.equal(d(42), '');
assert.equal(d('claude-opus-5-5', 'Opus Five'), 'Opus Five', "the server's own name wins");
assert.equal(d('claude-opus-5-5', '  '), 'Opus 5.5', 'a blank server name is ignored');
assert.equal(d('', 'Named'), 'Named');
console.log('PASS model_names');
