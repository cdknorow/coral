package server

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/cloud"
)

// #3966: a busy check (429) reaches the auth side as "no answer", so the session keeps its
// 10-minute grace (TestHubSessionChecks shows the grace and its end); a refusal still ends it.
func TestHubErrBusyIsUnanswered(t *testing.T) {
	assert.ErrorIs(t, hubErr(cloud.ErrSigninBusy), auth.ErrHubUnanswered)
	assert.ErrorIs(t, hubErr(cloud.ErrSigninUnanswered), auth.ErrHubUnanswered)
	err := hubErr(cloud.ErrSigninRefused)
	assert.False(t, errors.Is(err, auth.ErrHubUnanswered))
}
