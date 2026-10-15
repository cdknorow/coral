package clitext

// TakeRaw removes the --raw flag from a CLI's arguments and reports whether it was there. Only
// arguments before a "--" count, so a message after "--" can still be the text "--raw".
func TakeRaw(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	raw := false
	for i, a := range args {
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if a == "--raw" {
			raw = true
			continue
		}
		out = append(out, a)
	}
	return out, raw
}
