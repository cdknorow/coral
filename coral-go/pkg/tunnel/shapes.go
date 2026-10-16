package tunnel

import "strings"

// shapeSegment fills a "{}" in a route shape: 64 lowercase hex digits match both a {param}
// and a {hex64} segment.
var shapeSegment = strings.Repeat("0", 64)

// ShapeAllowed reports whether the tunnel lets any request of this route shape through at
// some tier: shape is a path with "{}" for each parameter, as tools/gen_route_queries.py
// writes routes_query.go. The generator lists query parameters only for such routes, so the
// table holds no entry for a route the tunnel refuses (#3942).
func ShapeAllowed(method, shape string) bool {
	return Classify(method, strings.ReplaceAll(shape, "{}", shapeSegment), "") != TierNever
}
