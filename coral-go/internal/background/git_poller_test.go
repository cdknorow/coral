package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestQueryChangedFiles_AFailedDiffIsAnErrorNotAnEmptyList(t *testing.T) {
	files, err := queryChangedFiles(context.Background(), t.TempDir()) // not a git repo
	assert.Error(t, err)
	assert.Nil(t, files)
}

func TestGitPollInterval(t *testing.T) {
	assert.Equal(t, 120*time.Second, GitPollInterval(map[string]string{}, 120))
	assert.Equal(t, 30*time.Second, GitPollInterval(map[string]string{"git_poll_interval_s": " 30 "}, 120))
	assert.Equal(t, time.Duration(0), GitPollInterval(map[string]string{"git_poll_interval_s": "0"}, 120))
	assert.Equal(t, 120*time.Second, GitPollInterval(map[string]string{"git_poll_interval_s": "abc"}, 120))
}
