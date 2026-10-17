// Views at 200% text on a touch phone, part 2 of 5 (the agent side panel drawers, a long list and a task with content): see text200_scenes.js for what is checked. Run against an isolated dev server.
require('./text200_scenes.js')('drawers').catch(e => { console.error(e.message); process.exitCode = 1; });
