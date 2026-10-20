package stage

import "time"

// The wait after a failure for a version (#5292, Security's section 9: never a loop that downloads the package more than once an hour):
// a network failure retries in 5 minutes, then 15, then an hour; a verification failure or a package that does not match waits an hour and doubles
// to a day. A newer version starts again.
const (
	KindNetwork = "network"
	KindVerify  = "verify"
)

func backoff(kind string, attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if kind == KindNetwork {
		switch attempts {
		case 1:
			return 5 * time.Minute
		case 2:
			return 15 * time.Minute
		}
		return time.Hour
	}
	d := time.Hour
	for i := 1; i < attempts && d < 24*time.Hour; i++ {
		d *= 2
	}
	return min(d, 24*time.Hour)
}
