package routes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// humanCtxTerm records the origin each send reached the terminal with.
type humanCtxTerm struct {
	*mockSessionTerminal
	origins []terminalinput.Origin
}

func (h *humanCtxTerm) SendInput(_ context.Context, o terminalinput.Origin, _, _, _, _ string) error {
	h.origins = append(h.origins, o)
	return nil
}

// #60 review N3: a person's text sent through the dashboard's chat or send box (deliverHumanSend,
// not the terminal's websocket) reaches the terminal with the Person origin, so the input
// debug log keeps its length only.
func TestHumanSendIsMarkedAsAPersonsInput(t *testing.T) {
	_, handler, mock, _ := setupSessionsTestServer(t)
	term := &humanCtxTerm{mockSessionTerminal: mock}
	handler.terminal = term
	require.NoError(t, handler.deliverHumanSend(context.Background(), "custom-60", "custom", "", "my password is hunter2"))
	require.NoError(t, term.SendInput(context.Background(), terminalinput.Unclassified, "custom-60", "coral's own", "custom", ""))
	assert.Equal(t, []terminalinput.Origin{terminalinput.Person, terminalinput.Unclassified}, term.origins)
}
