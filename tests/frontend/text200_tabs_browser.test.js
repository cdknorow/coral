// Views at 200% text on a touch phone, part 1 of 5 (the top tabs, the agent chat and the header glyphs): see text200_scenes.js for what is checked. Run against an isolated dev server.
require('./text200_scenes.js')('tabs').catch(e => { console.error(e.message); process.exitCode = 1; });
