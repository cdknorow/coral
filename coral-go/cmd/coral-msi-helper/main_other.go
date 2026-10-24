//go:build !windows

// coral-msi-helper is the Windows MSI's custom-action program (main_windows.go); elsewhere it does nothing.
package main

import "fmt"

func main() { fmt.Println("coral-msi-helper runs only in the Windows installer") }
