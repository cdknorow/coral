// Package dllpreload loads the system DLLs the webview library looks up by bare name, from the system folder, before it does (Windows;
// nothing elsewhere). Import it for its side effect, first, in a program that uses the webview.
//
// webview.h calls LoadLibraryW with a bare name for ntdll, user32, shcore and dwmapi. A bare name is searched for in the folder the
// program was started from before the system folder, and dwmapi is not a KnownDLL: where that folder is one a user can write to (the
// portable zip unpacked into Downloads) a dwmapi.dll placed beside coral-app.exe would be the one loaded (#5710 audit, finding 3).
// windows.NewLazySystemDLL loads from the system folder only (LOAD_LIBRARY_SEARCH_SYSTEM32), and a later LoadLibraryW of a name
// that is already loaded returns the module that is loaded instead of searching. The search path itself is not changed
// (SetDefaultDllDirectories would also change how WebView2's own loader finds its files).
package dllpreload

// Names are the DLLs preloaded.
var Names = []string{"ntdll.dll", "user32.dll", "shcore.dll", "dwmapi.dll"}
