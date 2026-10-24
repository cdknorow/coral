// What the Save toast says (#7041, UI/UX's #7036 finding 2): the rows that changed by name, a switch with its new state, three of them and then "and N more", and what applies only after a restart.
import assert from 'node:assert';
import { installDom, importUi } from './helpers/jsdom_env.mjs';

installDom({ mode: 'local' });
const w = await importUi('settings_saved_words');
const say = (payload, opts) => w.savedWords(payload, opts);
assert.equal(say({ show_scrollbars: false, git_diff_mode: 'main_head' }), 'Saved: Show scrollbars off, Git diff mode');
assert.equal(say({ show_scrollbars: true }), 'Saved: Show scrollbars on');
assert.equal(say({ agent_priority: 'normal' }), 'Saved: Run agents at lower priority off');
assert.equal(say({ agent_priority: 'low' }), 'Saved: Run agents at lower priority on');
// three, then the rest counted
assert.equal(say({ show_scrollbars: false, git_diff_mode: 'main_head', file_search_limit: '1000', terminal_font_size: '12', default_agent_type: 'codex' }), 'Saved: Show scrollbars off, Git diff mode, File search limit and 2 more');
assert.equal(say({ show_scrollbars: false, git_diff_mode: 'x', file_search_limit: '1', terminal_font_size: '12' }), 'Saved: Show scrollbars off, Git diff mode, File search limit and 1 more');
// one row that is several settings is one name: the theme pair, the CLI paths (claude, codex, agy and the gemini twin), the default models
assert.equal(say({ theme: 'light', custom_theme: '' }), 'Saved: Theme');
assert.equal(say({ cli_path_claude: '/x/claude', cli_path_agy: '/x/agy', cli_path_gemini: '/x/agy' }), 'Saved: CLI paths');
assert.equal(say({ default_model_agy: 'a', default_model_gemini: 'a', default_model_pi: 'b' }), 'Saved: Default models');
assert.equal(say({ artifact_retention_days: '30', artifact_retention_kinds: 'diffs' }), 'Saved: Automatic cleanup');
// what applies at the next start is said, in the same breath
assert.equal(say({ remote_access_enabled: true }, { restart: true }), 'Saved: Remote access on. Applies when Coral restarts.');
assert.equal(say({ show_scrollbars: true }, { restart: true }), 'Saved: Show scrollbars on');
// the rows saved apart (custom models, update choices) are named too; a payload with nothing known falls back to the plain words
assert.equal(say({}, { extra: ['Custom models'] }), 'Saved: Custom models');
assert.equal(say({ show_scrollbars: false }, { extra: ['Custom models'] }), 'Saved: Show scrollbars off, Custom models');
assert.equal(say({ some_new_key: 1 }), 'Settings saved');
assert.equal(say({}), 'Settings saved');
console.log('settings_saved_words: ok');
