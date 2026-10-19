package secrets

import "github.com/cdknorow/coral/pkg/redact"

// hubRedactor is the hub's redactor with the keyword rules on (the environment's switch aside: the tests that set it do so themselves).
func hubRedactor() redact.Redactor { return HubRedactor(func() bool { return true }) }
