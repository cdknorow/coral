// Views at 200% text on a touch phone, part 4 of 5 (a tablet and a landscape phone): see text200_scenes.js for what is checked. Run against an isolated dev server.
require('./text200_scenes.js')('wide').catch(e => { console.error(e.message); process.exitCode = 1; });
