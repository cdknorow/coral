/* How the subagent dialog reaches the task list and the shared task dialog (#3837). tasks.js owns both (renderBoardTaskList,
 * hideTaskDetailModal) and registers them here once; the subagent module imports this small module, which imports nothing of
 * tasks.js, so the two can live in separate files without an import cycle. */

let _view = null;

/** tasks.js registers the functions the subagent dialog calls back. */
export function registerTaskView(view) { _view = view; }

/** Redraw the board task list (nothing happens before tasks.js has registered). */
export function redrawBoardTaskList() { if (_view) _view.renderBoardTaskList(); }

/** Close the shared task dialog (nothing happens before tasks.js has registered). */
export function closeTaskDetailModal() { if (_view) _view.hideTaskDetailModal(); }
