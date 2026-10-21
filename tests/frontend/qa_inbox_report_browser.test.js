// TRIAL ONLY (#5774): runs the real-server suite and ends with exit 1 so the runner prints its whole output (checks and notes) in the CI log.
const realExit = process.exit.bind(process); process.exit = () => realExit(1);
require('./qa_inbox_real_browser.test.js');
