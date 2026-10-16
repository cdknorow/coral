// Package ptr holds the small string-pointer helpers Coral's stores and handlers share (#3824: each
// was written in several packages).
package ptr

// NonEmpty is a pointer to s, or nil when s is empty (an optional column or field left unset).
func NonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// NullIfEmpty is s for a SQL argument, or nil (NULL) when s is empty.
func NullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Value is what s points to, or "" when s is nil.
func Value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
