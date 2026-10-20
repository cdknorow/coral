package routes

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// ReplyToTask answers a task (#5463, coral-hub's inbox): one ordinary board message linked to the task (message.task_id) whose text starts with a mention of the task's creator,
// so the existing mention delivery notifies them. POST /api/board/{project}/tasks/{taskID}/reply {"content": "..."}
//
// It is the post route with a task: postMessageCore does everything a post does (the 256 KiB cap, cleaning, the new-board rule, the hub user as the sender and the Operator rule,
// stamping, the recorded hub action, waits and notification). The body has no subscriber_id and no as: the sender is whoever the request proves it is, exactly as for a
// post (Coral Hub: the stamped hub user; locally: the verified actor). The task is looked up by the URL's board first, so a task of another board is a 404 before anything is
// written or a board is made. The creator's mention is added only when it tags exactly the creator (board.TaskReplyMention); otherwise the reply is stored with its task_id, nobody
// is notified and the answer carries the warning creator_not_reachable. A reply never changes the task.
func (h *BoardHandler) ReplyToTask(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	taskID, err := strconv.ParseInt(chi.URLParam(r, "taskID"), 10, 64)
	if err != nil || taskID <= 0 {
		errBadRequest(w, "invalid task ID")
		return
	}
	task, err := h.bs.GetTask(r.Context(), project, taskID)
	if err != nil || task == nil {
		errNotFound(w, "Task not found")
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	derr := decodeJSON(r, &in)
	reply := &taskReply{taskID: taskID, creator: task.CreatedBy}
	msg, warnings, ok := h.postMessageCore(w, r, project, derr, postMessageBody{Content: in.Content}, reply)
	if !ok {
		return
	}
	notified := reply.notified
	if notified == nil {
		notified = []string{}
	}
	if reply.prefix == "" {
		warnings = append([]string{"creator_not_reachable"}, warnings...)
	}
	out := map[string]any{"ok": true, "message_id": msg.ID, "task_id": taskID, "notified": notified}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	writeJSON(w, http.StatusOK, out)
}
