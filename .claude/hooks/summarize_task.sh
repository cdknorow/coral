#!/usr/bin/env bash
# UserPromptSubmit hook: summarizes the submitted prompt with Claude
# and appends the result to .claude/task_summaries.local.md in the project root (untracked).

INPUT=$(cat)

PROMPT=$(echo "$INPUT" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(d.get('prompt', ''))
" 2>/dev/null)

CWD=$(echo "$INPUT" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(d.get('cwd', ''))
" 2>/dev/null)

# Nothing to summarize
[ -z "$PROMPT" ] && exit 0

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# An untracked file (the .gitignore lists it), or the path in CORAL_TASK_SUMMARIES: the summaries are private and never belong in the repository.
OUTPUT_FILE="${CORAL_TASK_SUMMARIES:-$PROJECT_ROOT/.claude/task_summaries.local.md}"

# Run in background so it doesn't block the prompt
(
  SUMMARY=$(claude -p "Summarize the following task in one concise sentence (max 20 words). Reply with only the sentence, no preamble.

Task: $PROMPT" 2>/dev/null)

  {
    echo "## $(date '+%Y-%m-%d %H:%M:%S')"
    echo ""
    echo "**Summary:** ${SUMMARY:-[summarization failed]}"
    echo ""
    echo "<details><summary>Full prompt</summary>"
    echo ""
    echo "$PROMPT"
    echo ""
    echo "</details>"
    echo ""
    echo "---"
    echo ""
  } >> "$OUTPUT_FILE"
) &

exit 0
