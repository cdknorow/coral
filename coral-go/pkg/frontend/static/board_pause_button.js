// The team board panel's Pause button, drawn in one place: board_chat.js (its own click and its load) and message_board.js (the Board page loading or pausing a board) both write it, and the page used to
// put the text "Pause Board" over the icon and leave the name stale (#5491). A leaf module, so the two do not import each other.

/** The pause button says what it will do: Pause (a pause icon) while reads are on, Resume (a play icon, in the warning colour) while the board is paused. */
export function paintPauseButton(btn, paused) {
    if (!btn) return;
    const icon = btn.querySelector('.material-icons');
    if (icon) icon.textContent = paused ? 'play_arrow' : 'pause';
    btn.setAttribute('aria-label', paused ? 'Resume board' : 'Pause board');
    btn.title = paused ? 'Resume board: agents see new messages again' : 'Pause board: stops agents from receiving new messages until resumed';
    btn.classList.toggle('mb-action-danger', paused);
}
