//go:build !relaunchfixture

package config

// BuildFlavor is "" in every build that ships (and every normal build): the relaunch test seam's build tag (#5452) is a compile-time difference only, never a setting.
const BuildFlavor = ""
