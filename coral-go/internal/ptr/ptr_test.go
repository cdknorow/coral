package ptr

import "testing"

func TestHelpers(t *testing.T) {
	if NonEmpty("") != nil || *NonEmpty("a") != "a" {
		t.Error("NonEmpty")
	}
	if NullIfEmpty("") != nil || NullIfEmpty("a") != "a" {
		t.Error("NullIfEmpty")
	}
	s := "b"
	if Value(nil) != "" || Value(&s) != "b" {
		t.Error("Value")
	}
}
