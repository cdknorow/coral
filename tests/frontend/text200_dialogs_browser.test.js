// Views at 200% text on a touch phone, part 3 of 5 (Settings, every dialog and the launch dialog): see text200_scenes.js for what is checked. Run against an isolated dev server.
require('./text200_scenes.js')('dialogs').catch(e => { console.error(e.message); process.exitCode = 1; });
