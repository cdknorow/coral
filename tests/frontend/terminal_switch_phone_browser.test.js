'use strict';
// Switching from Chat to Terminal renders the terminal every time, and right away (#4518): 1 and 2 at 390 px. The body is helpers/terminal_switch_common.js
// (read its header for what is checked); the suite was one file and is split so the shards run the parts side by side (#5027).
require('./helpers/terminal_switch_common')(['phone']);
