package agentenv

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3421: at startup Coral's own environment says this server, never what started it.
func TestUseServerEnv(t *testing.T) {
	for _, kv := range [][2]string{{"CORAL_PORT", "8420"}, {"CORAL_URL", "http://x:1"}, {"CORAL_DIR", "/p"}, {"CORAL_DATA_DIR", "/p"},
		{"CORAL_SESSION_NAME", "parent"}, {"CORAL_SESSION_ID", "sid"}, {"CORAL_SUBSCRIBER_ID", "Role"}, {"CORAL_SESSION_TOKEN", "tok"}} {
		t.Setenv(kv[0], kv[1])
	}
	UseServerEnv("0.0.0.0", 8583, "/tmp/own")
	for k, want := range map[string]string{"CORAL_PORT": "8583", "CORAL_HOST": "127.0.0.1", "CORAL_URL": "http://127.0.0.1:8583", "CORAL_DIR": "/tmp/own", "CORAL_DATA_DIR": "/tmp/own"} {
		assert.Equal(t, want, os.Getenv(k), k)
	}
	for _, k := range IdentityVars {
		_, set := os.LookupEnv(k)
		assert.False(t, set, k)
	}
	UseServerEnv("::1", 9000, "")
	assert.Equal(t, "http://[::1]:9000", os.Getenv("CORAL_URL"))
	_, set := os.LookupEnv("CORAL_DATA_DIR")
	assert.False(t, set, "no data folder known: not left from before")
}
