package routes

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// formatStalenessJS mirrors formatStaleness in static/render.js.
func formatStalenessJS(v any) string {
	seconds, ok := v.(float64)
	if !ok {
		return "Unknown"
	}
	switch {
	case seconds < 5:
		return "Just now"
	case seconds < 60:
		return fmt.Sprintf("%ds ago", int(math.Floor(seconds)))
	case seconds < 3600:
		return fmt.Sprintf("%dm ago", int(math.Floor(seconds/60)))
	default:
		return fmt.Sprintf("%dh ago", int(math.Floor(seconds/3600)))
	}
}

func TestListStaleness_DisplayUnchanged(t *testing.T) {
	values := []any{nil, "x", -3.7, -0.2, 0.0, 0.4, 4.999, 5.0, 5.01, 59.99, 60.0, 61.5, 119.9, 3599.99, 3600.0, 7199.5, 86400.3}
	for f := 0.0; f < 3*86400; f += 0.37 * (1 + f/50) {
		values = append(values, f)
	}
	for _, v := range values {
		require.Equal(t, formatStalenessJS(v), formatStalenessJS(listStaleness(v)), "value %v", v)
	}
}

func TestListStaleness_IdleSessionStableAcrossTicks(t *testing.T) {
	// An idle agent seen on consecutive 5s ticks within the same minute.
	require.Equal(t, listStaleness(600.2), listStaleness(605.2))
	require.Equal(t, listStaleness(600.2), listStaleness(659.9))
	require.NotEqual(t, listStaleness(659.9), listStaleness(660.1))
	require.Equal(t, listStaleness(7300.0), listStaleness(9000.0))
}
