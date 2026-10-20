'use strict';
// Switching from Chat to Terminal renders the terminal every time, and right away (#4518): after switching to another agent, and an agent switch made in Terminal view at 390 px. The body is helpers/terminal_switch_common.js
// (read its header for what is checked); the suite was one file, then three, and is now seven so the shards run the parts side by side (#5027, #5443).
require('./helpers/terminal_switch_common')(['phone:agent']);
