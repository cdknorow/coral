// Package redact removes credentials from text before it leaves the computer: well-known token
// shapes, Authorization header values, a URL's password, values named as secrets, and PEM private
// keys. It imports nothing from Coral, so it can stand on its own.
//
// Tokens redacts text held whole, and Writer redacts a stream: any split of the input into
// writes gives the same output as the whole. JSON and Reply redact a JSON document by its
// structure (each string with its key), so escaping can't hide a token and the output stays
// valid JSON. Cut picks a place to shorten text that never falls inside a token.
//
// Every match is replaced by Marker.
package redact
