# shellcheck shell=bash
# coral_find_node: sets NODE to the Node.js the stress runners use, the same way in every environment
# (#3926: one runner found it and another didn't, because nvm's node is on PATH only in some shells):
#   1. CORAL_TEST_NODE, when set (it must be executable);
#   2. node on PATH;
#   3. epoch's documented install, $HOME/node-install/node/bin/node (see the team's internal notes).
# Returns 1 with a message on stderr when none is found.
coral_find_node() {
    if [[ -n "${CORAL_TEST_NODE:-}" ]]; then
        [[ -x "$CORAL_TEST_NODE" ]] || { echo "CORAL_TEST_NODE is not executable: $CORAL_TEST_NODE" >&2; return 1; }
        NODE=$CORAL_TEST_NODE
    elif command -v node >/dev/null 2>&1; then
        NODE=$(command -v node)
    elif [[ -x "$HOME/node-install/node/bin/node" ]]; then
        NODE=$HOME/node-install/node/bin/node
    else
        echo "Node.js is required: set CORAL_TEST_NODE, put node on PATH, or install it as the team's internal notes say" >&2
        return 1
    fi
    export NODE
}
