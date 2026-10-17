// Views at 200% text on a touch phone, part 5 of 5 (the light theme): see text200_scenes.js for what is checked. Run against an isolated dev server.
require('./text200_scenes.js')('light').catch(e => { console.error(e.message); process.exitCode = 1; });
