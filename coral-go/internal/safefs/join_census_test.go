package safefs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A name that is not the program's own (read from a file in the data folder, a database row, a message from another Coral, a request or an agent-written file) must
// not be joined onto a folder and opened without being checked: "../../x" leaves the folder (#5305, the class behind LoadStaged's asset name, a pending record's id,
// a team worktree's board name). This census holds every function of the module that joins a non-literal name with filepath.Join or path.Join, with how its name
// is made safe. A new function, or a new join in one, fails until it is reviewed and added here: check the name (safefs.CheckName, IsSafeID, objectNameRe, a UUID),
// resolve and contain it (safefs.Within, Resolve, OpenRoot, os.Root), or say why it is the program's own.
type joinClass string

const (
	joinFixed     joinClass = "fixed"     // names from a directory listing, or numbers and hashes the program makes
	joinID        joinClass = "id"        // an id checked (objectNameRe, IsSafeID, IsSessionID, a canonical UUID) before the join
	joinContained joinClass = "contained" // resolved and compared with the folder it must stay in, or opened through a Root
	joinOwn       joinClass = "own"       // a name the program makes or checks itself (a constant, a slug, a sanitised name)
)

type joinEntry struct {
	n     int // how many non-literal joins the function holds
	class joinClass
	why   string
}

// joinSites is "file<TAB>function" -> its joins, by file relative to coral-go.
var joinSites = map[string]joinEntry{
	"internal/ptymanager/command_delivery_windows.go\tcommandInvocation":                     {1, joinOwn, "the Windows command handoff script (#25 G2B): the name is constant text plus a random number written in digits only (no separator or dot-dot can occur), in the launch temp folder (private to this user and SYSTEM); created O_EXCL through secfile.OpenNoFollow, so a link or an existing file at the path refuses"},
	"tools/coral-agents-token/keyfile.go\tkeyStore.keyPath":                                  {1, joinOwn, "the App key's file name, a constant, in the owner-only key folder (tools/coral-agents-token, never shipped); the helper checks the folder and the file (0700, 0600, owner, no link) and opens it without following a link"},
	"tools/coral-agents-token/keyfile.go\tkeyStore.cachePath":                                {1, joinOwn, "the cached token's file name, made by cacheName from the installation number, the allowed repository and the allowed permission set (all checked by request.validate before any join), in the cache folder under the key folder; opened without following a link and checked 0600 and owner"},
	"internal/updater/testrig/cmd/rehearsal-update/main.go\trun":                             {1, joinOwn, "the update rehearsal's own test program (never released and never linked by a program: a census in internal/updater/testrig): the staging folder, constant names under the rehearsal's own data folder given on its command line (--restage removes it); no request names it"},
	"internal/updater/trigger/launch_windows.go\tLaunch":                                     {2, joinOwn, "the updater's own places: the data folder's constant updates and apply folders, and the constant coral-update.exe beside the program this server runs from (resolved, never a request's); nothing in a request names any of it (#166)"},
	"internal/updater/trigger/launch_windows.go\trunInstalledStatus":                         {1, joinOwn, "the constant coral-update.exe beside the program this server runs from (resolved, never a request's), run read-only as \"status --json\" with a timeout and a size limit (#5946)"},
	"internal/stripstatic/stripstatic.go\tGenerate":                                          {2, joinContained, "the strip-static overlay (#6208): the replacement file is the static file's own relative path (from a directory walk under the static folder, taken with filepath.Rel) under the folder this run made for the overlay (owner-only, -out), and Validate refuses every key but a JS or CSS file of the static tree and every target outside that folder, in the overlay actually used; the overlay file itself is a constant name in that folder"},
	"internal/updater/trigger/marker.go\tWriteMarker":                                        {1, joinOwn, "the updates folder: a constant name under the data folder"},
	"internal/updater/trigger/marker.go\tmarkerPath":                                         {1, joinOwn, "the install marker's constant file name in the updates folder under the data folder; opened without following a link (secfile.OpenNoFollow)"},
	"internal/updater/trigger/staged.go\tStagedMSI":                                          {4, joinID, "the staging folder is a constant under the data folder; the version folder is the one entry of its listing that matched the version pattern; the asset is the record's, checked to be a plain .msi file name (no separator, no leading dot) before it is joined; only Lstat and Open of the record, never a write"},
	"cmd/coral-update/status.go\tcollectStatus":                                              {3, joinOwn, "the updater's own files under the data folder: the updates folder, state.json and run.json, constant names, only read (Lstat, a no-follow private read), nothing written"},
	"internal/updater/runrecord/restart.go\tRead":                                            {1, joinOwn, "the run record's file name, a constant, in the updates folder under the data folder; opened without following a link"},
	"internal/server/routes/relaunch_fixture.go\treadFixture":                                {1, joinOwn, "the relaunch test seam (#5452, a relaunchfixture build only): the fixed file name relaunch.json in the owner-only folder the test named; opened without following a link, read-only"},
	"internal/server/routes/relaunch_fixture.go\tfixtureUpdater.Apply":                       {1, joinOwn, "the relaunch test seam (#5452): the fixed marker name in the same owner-only folder, written by rename of a temp file made there"},
	"cmd/coral-agent/artifact_download.go\topenLocalArtifact":                                {1, joinID, "the artifact's own digest: artifactDigest checked it is 64 hexadecimal digits (and lower-cased it) before it names the object file under the data folder's artifacts/objects; opened without following a link, and the SHA-256 of what is read must equal it"},
	"cmd/coral-update/main.go\trun":                                                          {1, joinOwn, "the updates folder: a constant name under the data folder, plus the constant \"tuf\""},
	"internal/windowmark/windowmark.go\tSet":                                                 {1, joinFixed, "the marker's own constant name (FileName) in the data folder: no input names it"},
	"internal/windowmark/windowmark.go\tClear":                                               {2, joinFixed, "the marker's own constant name (FileName) in the data folder: no input names it"},
	"internal/windowmark/windowmark.go\tPresent":                                             {1, joinFixed, "the marker's own constant name (FileName) in the data folder: no input names it"},
	"internal/updater/apply/apply.go\tRun":                                                   {3, joinID, "the updates and apply folders and the ready marker are constants; the staged version folder is the verified record's version (verify.ParseVersion)"},
	"internal/updater/install/install.go\tMacManaged":                                        {2, joinOwn, "the prefixes are the default places and the one the environment names (absolute, cleaned); the folder names are constants and the app name is the running app's own base name; the result is only matched against, never opened for writing"},
	"internal/updater/apply/apply.go\twriteReady":                                            {1, joinOwn, "the updates folder and the constant marker name"},
	"internal/updater/apply/apply.go\tcopyVerified":                                          {1, joinID, "joins the private work folder with the record's asset name, which LoadStaged checked to be a plain file name"},
	"internal/updater/apply/apply.go\treadJournal":                                           {1, joinOwn, "the journal's file name, a constant, in the updates folder"},
	"internal/updater/apply/apply.go\twriteJournal":                                          {1, joinOwn, "the journal's file name, a constant, in the updates folder"},
	"internal/updater/apply/apply.go\tremoveJournal":                                         {1, joinOwn, "the journal's file name, a constant, in the updates folder"},
	"internal/updater/apply/apply.go\trecoverInterrupted":                                    {1, joinID, "the staged version folder named by the staged record itself (LoadStaged: ParseVersion, equal to the journal's record), never by the journal's own text"},
	"internal/updater/apply/apply_msi.go\tRunMSI":                                            {2, joinID, "the updates folder (a constant under the data folder), and the staged version folder removed after a good install, named by the verified record's version (verify.ParseVersion, re-verified by stage.Reverify)"},
	"internal/updater/apply/apply_msi.go\trecoverMSI":                                        {1, joinID, "the staged version folder named by the staged record itself (LoadStaged: ParseVersion, the same version and package hash as the journal's record), never by the journal's own text"},
	"internal/updater/apply/apply_msi.go\twriteMSIJournal":                                   {1, joinOwn, "the MSI journal's file name, a constant, in the updates folder; written privately (verify.WritePrivate)"},
	"internal/updater/apply/apply_msi.go\treadMSIJournal":                                    {1, joinOwn, "the MSI journal's file name, a constant, in the updates folder; read without following a link (verify.ReadPrivate)"},
	"internal/updater/apply/apply_msi.go\tremoveMSIJournal":                                  {1, joinOwn, "the MSI journal's file name, a constant, in the updates folder"},
	"internal/updater/apply/linux.go\tLinux.Unsupported":                                     {2, joinFixed, "the install's licenses folder (a constant) and names from a listing of the install's parent folder"},
	"internal/updater/apply/linux.go\tLinux.Verify":                                          {3, joinFixed, "names from a listing of the new folder, the licenses constant, and the program names of linuxPrograms"},
	"internal/updater/apply/linux.go\tLinux.runRecord":                                       {1, joinOwn, "the run record's file name, a constant, in the updates folder"},
	"internal/updater/apply/linux.go\tLinux.running":                                         {1, joinFixed, "a pid from a listing of /proc (parsed as a number) and the constant exe"},
	"internal/updater/apply/linux.go\tLinux.sameAsUnpacked":                                  {1, joinID, "a path the strict extractor wrote (plain components, an allowed top name or one licenses/ tree), kept in the unpack's own table"},
	"internal/updater/apply/linux.go\tlinuxNewName":                                          {1, joinOwn, "a hidden sibling of the install this program runs from, made from its own folder name"},
	"internal/updater/apply/mac.go\tMac.Build":                                               {1, joinOwn, "the private work folder's constant mount name, and the app name inside the mounted image taken from the install's own bundle name"},
	"internal/updater/apply/mac.go\tnewName":                                                 {1, joinOwn, "a hidden sibling of the install this program runs from, made from its own bundle name"},
	"internal/updater/runrecord/runrecord.go\tWrite":                                         {1, joinOwn, "the updates folder and the run record's file name, both constants, under the data folder"},
	"internal/updater/stage/lock.go\tCheckRunning":                                           {1, joinOwn, "the check lock's file name, a constant, in the updates folder"},
	"internal/server/routes/shared_images.go\tSessionsHandler.removePastedCopy":              {1, joinID, "the id was read back by readUploadRecord (objectNameRe) and the session id is checked by uploadsDir (IsSafeID, \"\" when unsafe) before the join (#5284)"},
	"internal/server/routes/shared_images.go\totherReferences":                               {2, joinID, "the board folder names pass boardDirName, the kinds are constants, and its only caller (removePastedCopy) passes an id readUploadRecord already checked (#5284)"},
	"internal/server/routes/shared_images.go\treadUploadRecord":                              {1, joinID, "the id is checked with objectNameRe and the session id by uploadsDir before the join (#5284)"},
	"internal/updater/stage/load.go\tLoadStaged":                                             {1, joinOwn, "the staging folder: constant names under the data folder; the version folder is loadStagedIn's (#5292, #5301)"},
	"internal/updater/stage/load.go\tloadStagedIn":                                           {4, joinID, "the version folder name passes verify.ParseVersion and equals the record's version, the record's asset name is a plain file name (no separator, no dot-prefix), the other names are constants; root is the staging or the rollback folder, both constants under the updates folder (#5292, #5301, #165)"},
	"internal/updater/stage/load.go\tReverify":                                               {1, joinID, "joins the staged folder with the record's asset name, which LoadStaged checked to be a plain file name and the result must equal the staged package path (#5292, #5301)"},
	"internal/updater/stage/lock.go\tapplyRunning":                                           {1, joinOwn, "the apply lock's file name, a constant, in the updater's folder"},
	"internal/updater/stage/lock.go\ttakeLock":                                               {1, joinOwn, "the lock's file name: its callers pass the check's or the apply's lock constant"},
	"internal/updater/stage/stage.go\tCheck":                                                 {1, joinOwn, "the updater's folder name, a constant, under the data folder"},
	"internal/updater/stage/stage.go\tchecker.prune":                                         {1, joinFixed, "a name from a directory listing of the staging folder, removed under it"},
	"internal/updater/stage/stage.go\tchecker.stage":                                         {1, joinOwn, "the staging folder, a constant under the updates folder; the version folder is stageInto's (#5292)"},
	"internal/updater/stage/stage.go\tchecker.stageInto":                                     {5, joinID, "the version is the verified record's (verify.ParseVersion), the asset name is the one the program builds for it, the other names are constants; root is the staging or the rollback folder, both constants under the updates folder (#5292, #165)"},
	"internal/updater/stage/rollback.go\tchecker.keepRollback":                               {1, joinOwn, "the rollback folder, a constant, under the updates folder (#165)"},
	"internal/updater/stage/rollback.go\tLoadRollback":                                       {1, joinOwn, "the rollback folder: constant names under the data folder; the version folder is loadStagedIn's, which must also be the installed version asked for (#165)"},
	"internal/updater/stage/rollback.go\tReverifyRollback":                                   {1, joinID, "compares, never opens: the kept folder joined with the re-verified record's asset name must equal the package path loadStagedIn checked (#165)"},
	"cmd/coral-update/apply_windows.go\twindowsMSIConfig":                                    {1, joinOwn, "the updates folder, a constant under the data folder, for state.json's revocations (verify.LoadState reads it privately) (#165)"},
	"internal/server/routes/updates_dir.go\tupdatesDir.read":                                 {1, joinOwn, "a state file name the callers pass as a constant (ack.key, ack_used.json, relaunch.json, relaunch_audit.json) in the updates folder"},
	"internal/server/routes/updates_dir.go\tupdatesDir.remove":                               {1, joinOwn, "a state file name the callers pass as a constant (ready-to-swap.json) in the updates folder"},
	"internal/server/routes/updates_dir.go\tupdatesDir.write":                                {1, joinOwn, "a state file name the callers pass as a constant (ack.key, ack_used.json, relaunch.json, relaunch_audit.json) in the updates folder"},
	"internal/server/routes/relaunch_real.go\tNewRealUpdater":                                {1, joinOwn, "the data folder the server was started with and the constant folder name updates"},
	"internal/server/routes/relaunch_real.go\tprogramPath":                                   {1, joinOwn, "the folder of the running program (symlinks resolved) and the constant file name coral-update: no request or setting names it"},
	"internal/server/routes/system_update_relaunch_wire.go\tNewUpdateRelaunchHandler":        {1, joinOwn, "the updates folder name, a constant, under the data folder"},
	"internal/updater/verify/state.go\tAckStore.Use":                                         {1, joinOwn, "the used-token file name, a constant, in the updater's folder"},
	"internal/updater/verify/state.go\tLoadState":                                            {1, joinOwn, "the state file name, a constant, in the updater's folder"},
	"internal/updater/verify/state.go\tSaveState":                                            {1, joinOwn, "the state file name, a constant, in the updater's folder"},
	"cmd/coral-app/loghint.go\tserverLogPath":                                                {2, joinOwn, "a program-made or constant name in its own folder (mock-agent is a test tool; launch-coral builds names from checked ids)"},
	"cmd/coral-board/state.go\tstateFilePath":                                                {1, joinOwn, "the session name goes through hooks.BoardStateFileName, which makes every path separator \"_\""},
	"cmd/coral-board/task.go\tseenRoundsPath":                                                {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"cmd/coral-hook-agentic-state/main.go\tagyStepOutputResult":                              {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"cmd/coral-hook-message-check/main.go\tloadBoardState":                                   {1, joinOwn, "the session name goes through hooks.BoardStateFileName, which makes every path separator \"_\""},
	"cmd/coral/password.go\tresetOffline":                                                    {1, joinOwn, "a program-made or constant name in its own folder (mock-agent is a test tool; launch-coral builds names from checked ids)"},
	"cmd/launch-coral/main.go\tlaunchAgentSessions":                                          {2, joinOwn, "a program-made or constant name in its own folder (mock-agent is a test tool; launch-coral builds names from checked ids)"},
	"cmd/mock-agent/main.go\tcoralBoard":                                                     {1, joinOwn, "a program-made or constant name in its own folder (mock-agent is a test tool; launch-coral builds names from checked ids)"},
	"cmd/mock-agent/main.go\tmain":                                                           {4, joinOwn, "a program-made or constant name in its own folder (mock-agent is a test tool; launch-coral builds names from checked ids)"},
	"cmd/mock-agent/rawinput.go\trunRawInput":                                                {1, joinOwn, "the raw-input record <session id>.raw in the mock agent's own folder (the same name main.go uses for its other files; mock-agent is a test tool; launch-coral builds the id from checked ids)"},
	"cmd/mock-agent/transcript.go\tnewTranscript":                                            {2, joinOwn, "a program-made or constant name in its own folder (mock-agent is a test tool; launch-coral builds names from checked ids)"},
	"internal/secfile/pathwriters.go\twalker.walk":                                           {1, joinOwn, "the path being examined, one component at a time as the system would resolve it (#5899): each step is only Lstat-ed, read as a link or checked for who can write it, never opened, written or run; a link in it is read and its target walked in turn, which is the point"},
	"internal/agent/agent.go\tCleanupTempFiles":                                              {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/agent/agy.go\tAgyAgent.ExtractSessions":                                        {2, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/agent/claude.go\tClaudeAgent.ExtractSessions":                                  {2, joinID, "the session id is checked (IsResumeID, a canonical UUID) before it is joined; the project folder name has its separators replaced"},
	"internal/agent/claude.go\tClaudeAgent.PrepareResume":                                    {5, joinID, "the session id is checked (IsResumeID, a canonical UUID) before it is joined; the project folder name has its separators replaced"},
	"internal/agent/claude.go\tcopyDir":                                                      {2, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/agent/codex.go\tCodexSessionSaved":                                             {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/agent/hookcmd.go\thookToolPath":                                                {1, joinOwn, "a coral-hook-* name constant from Coral's callers, refused if it holds a separator"},
	"internal/agent/pi.go\tPiAgent.LaunchCommand":                                            {1, joinID, "the session id is checked (IsResumeID, a canonical UUID) before it is joined; the project folder name has its separators replaced"},
	"internal/agent/shell.go\tFindCLIInCommonPaths":                                          {7, joinOwn, "a CLI or tool name constant"},
	"internal/agent/tempsweep.go\tSweepStaleTempFiles":                                       {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/auth/login_token.go\tKeyStore.WriteLoginTokenFile":                             {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/auth/login_token.go\tRemoveLoginTokenFile":                                     {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/auth/password.go\tCreatePasswordHash":                                          {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/auth/password.go\tReadPasswordHash":                                            {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/auth/password.go\tRemovePasswordHash":                                          {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/auth/password.go\tWritePasswordHash":                                           {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/auth/password_startup.go\tKeyStore.ApplyRevokeMarker":                          {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/background/git_poller.go\tqueryChangedFilesFrom":                               {1, joinContained, "a path git printed, relative to the working directory, only stat-ed"},
	"internal/background/knowledge.go\tLoadAgentKnowledge":                                   {1, joinOwn, "slugify or ValidKnowledgeTeam: a plain name, no separator, no device name"},
	"internal/background/knowledge.go\tReadKnowledgeBundle":                                  {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/background/knowledge.go\tWriteKnowledgeFile":                                   {1, joinOwn, "slugify or ValidKnowledgeTeam: a plain name, no separator, no device name"},
	"internal/background/knowledge.go\tknowledgeTeamDir":                                     {1, joinOwn, "slugify or ValidKnowledgeTeam: a plain name, no separator, no device name"},
	"internal/background/knowledge.go\twriteKnowledgeBundle":                                 {1, joinOwn, "slugify or ValidKnowledgeTeam: a plain name, no separator, no device name"},
	"internal/background/scheduler.go\tJobScheduler.runWorktreeDir":                          {1, joinOwn, "a run id number and the base name of the repo path"},
	"internal/background/token_poller.go\tresolvePiSessionPath":                              {1, joinID, "transcriptlink.IsSessionID or safefs.IsSafeID before the join"},
	"internal/background/workflow_runfolder.go\topenRunFolder":                               {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/background/workflow_runfolder.go\trunFiles.step":                               {1, joinContained, "opened through an os.Root over the run folder"},
	"internal/background/workflow_runfolder.go\tstepFiles.appendNote":                        {1, joinContained, "opened through an os.Root over the run folder"},
	"internal/background/workflow_runfolder.go\tstepFiles.create":                            {1, joinContained, "opened through an os.Root over the run folder"},
	"internal/background/workflow_runfolder.go\tstepFiles.open":                              {1, joinContained, "opened through an os.Root over the run folder"},
	"internal/background/workflow_runfolder.go\tstepFiles.writeArtifact":                     {1, joinContained, "opened through an os.Root over the run folder"},
	"internal/background/workflow_runfolder.go\tstepFiles.writeNew":                          {1, joinContained, "opened through an os.Root over the run folder"},
	"internal/background/workflow_runner.go\tRemoveRunDirs":                                  {2, joinOwn, "run and step numbers; files named by the step are under the step folder"},
	"internal/background/workflow_templates.go\tWorkflowRunner.buildStepEnv":                 {3, joinOwn, "run and step numbers; files named by the step are under the step folder"},
	"internal/cliauth/transport.go\treadPrivateFile":                                         {1, joinContained, "the file is opened through safefs.OpenRoot(dir) (Root.Open), so the name can't leave the folder; the joined path is only for messages"},
	"internal/cloud/clitoken.go\tReadCLIToken":                                               {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/cloud/clitoken.go\tRemoveCLIToken":                                             {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/cloud/clitoken.go\tWriteCLIToken":                                              {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/cmdhandoff/sweep.go\tsweepDir":                                                 {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/dbcrypt/dbcrypt.go\tLoad":                                                      {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/dbcrypt/dbcrypt.go\tMigrationRecoveryPath":                                     {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/dbcrypt/dbcrypt.go\tSave":                                                      {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/dbcrypt/dbcrypt.go\tkeyPath":                                                   {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/executil/editor.go\tEditor.macApp":                                             {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/executil/editor.go\teditorCommand":                                             {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/gitutil/gitutil.go\tResolveGitRoot":                                            {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/hooks/cache.go\tTaskCacheWrite":                                                {1, joinOwn, "taskCacheName: a validated task id"},
	"internal/hooks/cache.go\topenCacheFile":                                                 {1, joinOwn, "taskCacheName: a validated task id"},
	"internal/hooks/cache.go\tpruneTaskCache":                                                {1, joinOwn, "taskCacheName: a validated task id"},
	"internal/inputlog/inputlog.go\tSetup":                                                   {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/jsonl/transcript_paths.go\tresolveAgyTranscriptFromHistory":                    {1, joinID, "transcriptlink.IsSessionID or safefs.IsSafeID before the join"},
	"internal/jsonl/transcript_paths.go\tresolveAgyTranscriptMode":                           {3, joinID, "transcriptlink.IsSessionID or safefs.IsSafeID before the join"},
	"internal/jsonl/transcript_paths.go\tresolveClaudeTranscript":                            {2, joinID, "transcriptlink.IsSessionID or safefs.IsSafeID before the join"},
	"internal/jsonl/transcript_paths.go\tresolveCodexTranscriptCtx":                          {1, joinID, "the glob's name holds the session id, which transcriptlink.IsSessionID checked to be a canonical UUID before anything else (the first lines of the function)"},
	"internal/naming/naming.go\tLogFile":                                                     {1, joinID, "transcriptlink.IsSessionID or safefs.IsSafeID before the join"},
	"internal/peercred/peercred.go\tlookup":                                                  {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/safefs/safefs.go\tRoot.WriteFileUnless":                                        {2, joinContained, "the containment code itself"},
	"internal/safefs/safefs.go\tWithin":                                                      {1, joinContained, "the containment code itself"},
	"internal/safefs/safefs.go\tresolveExisting":                                             {2, joinContained, "the containment code itself"},
	"internal/server/data_perms.go\ttightenDataPermissions":                                  {3, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/server/routes/agent_docs.go\tAgentDocsHandler.Get":                             {1, joinContained, "an embedded fs.FS, which refuses a name that is not a valid path"},
	"internal/server/routes/artifact_comments.go\tSessionsHandler.commentArtifact":           {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifact_readers.go\tSessionsHandler.artifactReaders":            {1, joinID, "an object id is checked with validArtifactID (64 lower-case hex digits) before the join (#5305)"},
	"internal/server/routes/artifact_owner.go\tartifactRecordsOf":                            {2, joinID, "the read check (#5577): a received board folder is checked with boardDirName (32 hex digits) and a record file must be exactly <64 hex digits>.json (objectNameRe) before each join; the session folders go through uploadsDir (IsSafeID) and readUploadRecord; folders are only read, never followed"},
	"internal/server/routes/artifacts_list_all.go\tlistAllArtifactRows":                      {4, joinID, "the machine-wide list (#5575): an object id is checked with objectNameRe (64 hex digits) and the file name must be exactly <id>.json, a received board folder with boardDirName, and a session folder through uploadsDir (IsSafeID) before each join; the folders are read only, never followed"},
	"internal/server/routes/artifact_prune.go\tSessionsHandler.closedDiffs":                  {2, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/server/routes/artifact_prune.go\tSessionsHandler.keepArtifactRoute":            {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifact_prune.go\tSessionsHandler.objectCandidates":             {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifact_prune.go\tSessionsHandler.sweepDiffObjects":             {2, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/server/routes/artifact_prune.go\tdiffObjectInodes":                             {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/server/routes/artifact_prune.go\tnewestRecordFor":                              {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifact_prune.go\tobjectNewestUse":                              {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifact_prune.go\tscanCrossTeamRecordRefs":                      {2, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifact_prune.go\tuploadRecordTimes":                            {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/server/routes/artifact_storage.go\tSessionsHandler.allDiffs":                   {2, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifact_store.go\tartifactStore.measureLocked":                  {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifacts.go\tSessionsHandler.openArtifactForRead":               {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifacts.go\tdeleteArtifactFilesLocked":                         {4, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifacts.go\tstoreArtifactVia":                                  {2, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifacts.go\tstoreArtifactFile":                                 {2, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/artifacts.go\tuploadsDir":                                        {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/auto_accept_risk.go\tchangeDir":                                  {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/auto_accept_risk.go\tfromDir":                                    {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/auto_accept_risk.go\tpathInside":                                 {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/auto_accept_risk.go\tresolvePath":                                {2, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/auto_accept_risk.go\ttextInside":                                 {1, joinContained, "path.Join of slash paths: text only, nothing is opened or resolved; the result is compared with the working folder's cleaned text (the folder this OS cannot resolve), so it only ever says inside for a path written under that folder"},
	"internal/server/routes/auto_attach.go\tAutoAttacher.load":                               {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/board.go\tBoardHandler.taskArtifactPath":                         {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/server/routes/changes_diff_filter.go\tSessionsHandler.generatePersistedDiff":   {2, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/cross_team_attach.go\tBoardHandler.boardRecordDir":               {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/cross_team_attach.go\tBoardHandler.crossTeamRecord":              {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/cross_team_attach.go\tBoardHandler.objectMeta":                   {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/cross_team_attach.go\tBoardHandler.readAttachment":               {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/cross_team_attach.go\tBoardHandler.writeCrossTeamRecord":         {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/cross_team_attach_relay.go\tBoardHandler.loadAttachment":         {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/cross_team_pending.go\tBoardHandler.noteAlsoReceived":            {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/cross_team_pending.go\tBoardHandler.promotePending":              {2, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/cross_team_pending.go\tBoardHandler.sweepPending":                {2, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/cross_team_pending.go\tBoardHandler.writePendingReceipt":         {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/file_bounds.go\tSessionsHandler.inHooksPath":                     {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/file_bounds.go\tinAgentHomeDir":                                  {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/file_bounds.go\tlistingVisible":                                  {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/file_explorer.go\tserveExplorerDirectory":                        {2, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/message_key.go\tloadOrCreateKeySecret":                           {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/server/routes/pinned_folders.go\tpinnedPath":                                   {1, joinContained, "the Operator's folder picker: operator-only, and only listed"},
	"internal/server/routes/received_cap.go\thasUploadRecord":                                {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/received_cap.go\tpendingRecordPaths":                             {2, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/received_cap.go\treceivedUsage":                                  {3, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/sessions_changes.go\tSessionsHandler.Diff":                       {2, joinContained, "resolved and compared with the folder it must stay in before it is opened"},
	"internal/server/routes/sessions_changes.go\tSessionsHandler.sessionChangesArtifactPath": {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/sessions_changes.go\tstoreDiffObject":                            {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/server/routes/sessions_files_content.go\tSessionsHandler.GetFileContent":       {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/sessions_files_content.go\tSessionsHandler.GetFileOriginal":      {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/sessions_files_content.go\tSessionsHandler.SaveFileContent":      {3, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/sessions_files_resolve.go\tSessionsHandler.resolveFileRef":       {4, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/sessions_files_search.go\tSessionsHandler.searchFilesDir":        {1, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/server/routes/sessions_launch_board.go\tremoveBoardStateFile":                  {1, joinOwn, "the session name goes through hooks.BoardStateFileName, which makes every path separator \"_\""},
	"internal/server/routes/sessions_launch_board.go\twriteBoardStateFile":                   {1, joinOwn, "the session name goes through hooks.BoardStateFileName, which makes every path separator \"_\""},
	"internal/server/routes/sessions_launch_team.go\tSessionsHandler.createTeamWorktree":     {1, joinOwn, "the board name is checked with ValidKnowledgeTeam before it names the worktree folder (#5305)"},
	"internal/server/routes/system_filesystem.go\texpandHome":                                {1, joinContained, "the Operator's folder picker: operator-only, and only listed"},
	"internal/server/routes/system_security.go\tSystemHandler.DatabaseSecurityStatus":        {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/server/routes/system_team_import.go\tSystemHandler.ImportTeam":                 {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/server/routes/task_artifact_vet.go\tBoardHandler.objectIsStored":               {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/task_artifact_vet.go\tBoardHandler.objectSidecarDigest":          {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/team_artifacts.go\tenrichTeamArtifact":                           {1, joinID, "an object id is checked with objectNameRe (64 hex digits) or a session id with IsSafeID before the join (#5305)"},
	"internal/server/routes/team_artifacts.go\tuploadDirCache.list":                          {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/server/routes/themes.go\tThemesHandler.ImportTheme":                            {1, joinOwn, "a name made safe (base name, safeNameRE, safefs.CheckName) before the join"},
	"internal/server/routes/themes.go\tThemesHandler.ListThemes":                             {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/server/routes/themes.go\tThemesHandler.SeedBundledThemes":                      {1, joinOwn, "a name made safe (base name, safeNameRE, safefs.CheckName) before the join"},
	"internal/server/routes/themes.go\tThemesHandler.safePath":                               {1, joinOwn, "a name made safe (base name, safeNameRE, safefs.CheckName) before the join"},
	"internal/server/routes/tracking.go\ttelemetryDisplayPath":                               {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/server/routes/uploads.go\tUploadFile":                                          {1, joinOwn, "a name made safe (base name, safeNameRE, safefs.CheckName) before the join"},
	"internal/server/routes/workflow.go\tWorkflowHandler.GetStepFile":                        {2, joinContained, "resolved and compared with the folder it must stay in (safefs.Within, Resolve, os.Root or a resolved-prefix test) before it is opened"},
	"internal/stallwatch/stallwatch.go\tWatchdog.dump":                                       {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/stallwatch/stallwatch.go\tWatchdog.prune":                                      {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/startup/compact.go\tCompact":                                                   {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/startup/orphan_files.go\tremoveOrphanProxyCA":                                  {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/startup/rebuild_search.go\tRebuildSearch":                                      {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/startup/session_state_sweep.go\tsweepSessionStateFiles":                        {2, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/startup/session_tokens.go\tsweepSessionTokenFiles":                             {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/startup/startup_data.go\trefuseKeyFileReplacement":                             {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/startup/startup_data.go\tresolveDatabaseKey":                                   {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/startup/strict_local_auth.go\tstrictLocalAuth":                                 {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/subagent/transcript.go\tDiscover":                                              {2, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/testguard/env.go\tisolateDataDir":                                              {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"internal/tmux/socket_path.go\tdefaultServerMayRun":                                      {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/tracking/milestones.go\tdisclosurePath":                                        {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/tracking/milestones.go\tmilestonesPath":                                        {1, joinOwn, "a fixed or program-made name in its own folder"},
	"internal/transcriptlink/codex.go\tBindCodex":                                            {2, joinContained, "safefs.Within and Resolve against the sessions folder; the thread id is a canonical UUID"},
	"internal/transcriptlink/codex.go\tBindCodexByMarker":                                    {4, joinFixed, "names from a directory listing, each matched against a fixed pattern first (four digits, two digits, rollout-<uuid>.jsonl) under the sessions folder; the file is then opened only through the sessions root (rolloutMarker)"},
	"internal/transcriptlink/codex.go\trolloutPath":                                          {1, joinContained, "safefs.Within and Resolve against the sessions folder; the thread id is a canonical UUID"},
	"internal/uploads/uploads.go\tDelete":                                                    {1, joinOwn, "a name made safe (base name, safeNameRE, safefs.CheckName) before the join"},
	"internal/uploads/uploads.go\tlist":                                                      {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
	"pkg/frontend/bundle.go\tembeddedFiles":                                                  {1, joinOwn, "a fixed or program-made name in its own folder"},
	"pkg/frontend/startup_modules.go\tmoduleGraph":                                           {1, joinOwn, "a fixed or program-made name in its own folder"},
	"pkg/secretbox/secretbox.go\tKeyFilePath":                                                {1, joinFixed, "names from a directory listing or numbers and hashes the program makes"},
}

func TestEveryJoinOfAVariableNameIsReviewed(t *testing.T) {
	root := filepath.Join("..", "..") // coral-go
	got := map[string]int{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) && p != root {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := c.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Join" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || (x.Name != "filepath" && x.Name != "path") {
					return true
				}
				for _, a := range c.Args[min(1, len(c.Args)):] {
					if _, lit := a.(*ast.BasicLit); !lit {
						got[rel+"\t"+joinFuncName(fd)]++
						break
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Counted per join with a non-literal name after the first argument: two joins in one function count twice.
	var problems []string
	for key, n := range got {
		e, ok := joinSites[key]
		switch {
		case !ok:
			problems = append(problems, "unreviewed join of a variable name: "+strings.ReplaceAll(key, "\t", " ")+": check the name, contain it, or say why it is the program's own, and add it to joinSites")
		case e.n != n:
			problems = append(problems, strings.ReplaceAll(key, "\t", " ")+": the function holds a different number of such joins than reviewed; review the new one and update joinSites")
		}
	}
	for key, e := range joinSites {
		if _, ok := got[key]; !ok {
			problems = append(problems, "joinSites entry matches no join any more (remove it): "+strings.ReplaceAll(key, "\t", " "))
		}
		if e.class == "" || e.why == "" {
			problems = append(problems, "joinSites entry has no class or reason: "+strings.ReplaceAll(key, "\t", " "))
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

func joinFuncName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}
