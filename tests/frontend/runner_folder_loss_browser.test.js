// Fixture for run_runner_folder_loss.mjs (#3727): it does nothing in a normal run. With CDP_RUNNER_FOLDER_LOSS=1 (the
// wrapper sets it) its first attempt deletes the runner's own temp folder (Coral's binary, the shards' data) and fails,
// so the runner has to replace the shard with the folder gone; the retry then checks the folder is back with coral in it.
'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

if (process.env.CDP_RUNNER_FOLDER_LOSS !== '1') {
    console.log('runner_folder_loss: nothing to do outside run_runner_folder_loss.mjs');
    process.exit(0);
}
const runDir = path.dirname(path.dirname(process.env.CORAL_TEST_LOG)); // <run folder>/shardN/coral.log
const marker = path.join(os.tmpdir(), `coral-runner-folder-loss-${process.ppid}`); // keyed by the runner that started this suite
if (!fs.existsSync(marker)) {
    fs.writeFileSync(marker, '');
    fs.rmSync(runDir, { recursive: true, force: true, maxRetries: 20, retryDelay: 50 });
    console.log(`deleted the run folder ${runDir}`);
    process.exit(1);
}
fs.rmSync(marker, { force: true });
const back = fs.existsSync(path.join(runDir, 'coral'));
console.log(`the run folder ${runDir} ${back ? 'is back, with coral in it' : 'is NOT back'}`);
process.exit(back ? 0 : 1);
