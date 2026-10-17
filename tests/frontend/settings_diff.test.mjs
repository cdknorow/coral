// Unit test for what the Settings dialog sends on Save (no browser):
//   node settings_diff.test.mjs
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const { changedSettings } = await import(pathToFileURL(path.resolve(here, '../../coral-go/pkg/frontend/static/settings_diff.js')).href);

const GIB = 1024 ** 3;
const base = { cli_path_claude: '/opt/claude', cli_path_codex: '/opt/codex', terminal_font_size: '13', fit_pane_width: 'True', theme: 'dark', agent_priority: 'low' };

// Nothing changed: nothing is sent, CLI paths included.
assert.deepEqual(changedSettings({ ...base, fit_pane_width: true, theme: 'dark', custom_theme: '' }, base, 'dark'), {});

// A stale dialog (it opened before the path was saved elsewhere) holds an empty path and did not touch it: not sent.
const stale = { terminal_font_size: '13', fit_pane_width: 'True', theme: 'dark' };
assert.deepEqual(changedSettings({ cli_path_claude: '', cli_path_codex: '', terminal_font_size: '15', fit_pane_width: true, theme: 'dark', custom_theme: '' }, stale, 'dark'), { terminal_font_size: '15' });

// A path the person changed is sent; one they cleared on purpose is sent as empty.
assert.deepEqual(changedSettings({ cli_path_claude: '/new/claude', cli_path_codex: '/opt/codex' }, base), { cli_path_claude: '/new/claude' });
assert.deepEqual(changedSettings({ cli_path_claude: '', cli_path_codex: '/opt/codex' }, base), { cli_path_claude: '' });

// Booleans compare as booleans whatever their spelling; a changed one is sent.
assert.deepEqual(changedSettings({ fit_pane_width: true }, { fit_pane_width: 'True' }), {});
assert.deepEqual(changedSettings({ fit_pane_width: true }, { fit_pane_width: 'true' }), {});
assert.deepEqual(changedSettings({ fit_pane_width: false }, { fit_pane_width: 'True' }), { fit_pane_width: false });
assert.deepEqual(changedSettings({ fit_pane_width: false }, { fit_pane_width: true }), { fit_pane_width: false });

// Whitespace around a value is not a change.
assert.deepEqual(changedSettings({ default_working_dir: ' /x ' }, { default_working_dir: '/x' }), {});

// agy and gemini share one input: untouched, neither is sent (even when only gemini was saved); edited, both are.
const gem = { cli_path_gemini: '/opt/gemini' };
assert.deepEqual(changedSettings({ cli_path_agy: '/opt/gemini', cli_path_gemini: '/opt/gemini' }, gem), {});
assert.deepEqual(changedSettings({ cli_path_agy: '/opt/agy', cli_path_gemini: '/opt/agy' }, gem), { cli_path_agy: '/opt/agy', cli_path_gemini: '/opt/agy' });
assert.deepEqual(changedSettings({ default_model_agy: '', default_model_gemini: '' }, {}), {});
assert.deepEqual(changedSettings({ default_model_agy: 'm1', default_model_gemini: 'm1' }, { default_model_agy: 'm1' }), {});
assert.deepEqual(changedSettings({ default_model_agy: 'm2', default_model_gemini: 'm2' }, { default_model_agy: 'm1' }), { default_model_agy: 'm2', default_model_gemini: 'm2' });

// The theme select is one control for two settings.
assert.deepEqual(changedSettings({ theme: 'dark', custom_theme: 'Mine' }, { theme: 'dark', custom_theme: 'Mine' }, 'custom:Mine'), {});
assert.deepEqual(changedSettings({ theme: 'dark', custom_theme: '' }, { theme: 'dark', custom_theme: 'Mine' }, 'dark'), { theme: 'dark', custom_theme: '' });
assert.deepEqual(changedSettings({ theme: 'light', custom_theme: '' }, { theme: 'dark' }, 'light'), { theme: 'light', custom_theme: '' });
assert.deepEqual(changedSettings({ theme: 'dark', custom_theme: '' }, {}, 'dark'), {}, 'a never-saved theme shows as dark');

// The artifact store limit shows whole GiB: a limit that is not a whole number of them is unchanged until edited.
assert.deepEqual(changedSettings({ artifact_store_max_bytes: String(GIB) }, { artifact_store_max_bytes: String(GIB) }), {});
assert.deepEqual(changedSettings({ artifact_store_max_bytes: String(GIB) }, { artifact_store_max_bytes: String(GIB + 12345) }), {});
assert.deepEqual(changedSettings({ artifact_store_max_bytes: String(2 * GIB) }, { artifact_store_max_bytes: String(GIB) }), { artifact_store_max_bytes: String(2 * GIB) });
assert.deepEqual(changedSettings({ artifact_store_max_bytes: '0' }, {}), {});
// Retention fields read a missing setting as their defaults.
assert.deepEqual(changedSettings({ artifact_retention_days: '0', artifact_retention_kinds: 'diffs' }, {}), {});
assert.deepEqual(changedSettings({ artifact_retention_days: '30', artifact_retention_kinds: 'diffs' }, {}), { artifact_retention_days: '30' });

// A field the person typed in is sent even when what they typed equals what it showed (the page's copy may be stale).
assert.deepEqual(changedSettings({ cli_path_claude: '/opt/claude', cli_path_codex: '/opt/codex' }, base, undefined, ['cli_path_claude']), { cli_path_claude: '/opt/claude' });
assert.deepEqual(changedSettings({ cli_path_agy: '/opt/gemini', cli_path_gemini: '/opt/gemini' }, gem, undefined, ['cli_path_agy']), { cli_path_agy: '/opt/gemini', cli_path_gemini: '/opt/gemini' });

// With no baseline (the dialog was never opened) everything is sent, as before.
assert.deepEqual(changedSettings({ a: '1', cli_path_claude: '' }, null), { a: '1', cli_path_claude: '' });
assert.deepEqual(changedSettings({ a: '1' }, undefined), { a: '1' });
console.log('PASS settings diff');
