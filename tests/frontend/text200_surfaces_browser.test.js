// Views at 200% text on a touch phone, part 6 (#5058): the chat bar's model chip and its menu, the Cost tables, the Restart, New Agent and Agent Team dialogs (their section selector and
// footers) and the agent list's Display panel: see text200_scenes.js for what is checked. Run against an isolated dev server.
require('./text200_scenes.js')('surfaces').catch(e => { console.error(e.message); process.exitCode = 1; });
