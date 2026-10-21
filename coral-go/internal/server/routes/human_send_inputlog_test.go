package routes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/inputlog"
)

// humanCtxTerm records whether each send reached the terminal marked as a person's input.
type humanCtxTerm struct {
	*mockSessionTerminal
	human []bool
}

func (h *humanCtxTerm) SendInput(ctx context.Context, _, _, _, _ string) error {
	h.human = append(h.human, inputlog.IsHuman(ctx))
	return nil
}

// #60 review N3: a person's text sent through the dashboard's chat or send box (deliverHumanSend,
// not the terminal's websocket) reaches the terminal marked as a person's input, so the input
// debug log keeps its length only.
func TestHumanSendIsMarkedAsAPersonsInput(t *testing.T) {
	_, handler, mock, _ := setupSessionsTestServer(t)
	term := &humanCtxTerm{mockSessionTerminal: mock}
	handler.terminal = term
	require.NoError(t, handler.deliverHumanSend(context.Background(), "custom-60", "custom", "", "my password is hunter2"))
	require.NoError(t, term.SendInput(context.Background(), "custom-60", "coral's own", "custom", ""))
	assert.Equal(t, []bool{true, false}, term.human)
}
