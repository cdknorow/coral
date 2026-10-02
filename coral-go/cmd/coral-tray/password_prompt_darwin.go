//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <Cocoa/Cocoa.h>
#include <stdlib.h>

char *coralDatabasePassword(void) {
    NSAlert *alert = [[NSAlert alloc] init];
    alert.messageText = @"Coral — Unlock database";
    alert.informativeText = @"Enter your database password. It is used only before Coral opens its databases.";
    alert.alertStyle = NSAlertStyleInformational;
    [alert addButtonWithTitle:@"Unlock"];
    [alert addButtonWithTitle:@"Cancel"];
    NSSecureTextField *field = [[NSSecureTextField alloc] initWithFrame:NSMakeRect(0, 0, 360, 24)];
    alert.accessoryView = field;
    [alert layout];
    if ([alert runModal] != NSAlertFirstButtonReturn) return NULL;
    return strdup(field.stringValue.UTF8String);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func nativeDatabasePassword() (string, error) {
	value := C.coralDatabasePassword()
	if value == nil {
		return "", fmt.Errorf("database unlock cancelled")
	}
	defer C.free(unsafe.Pointer(value))
	return C.GoString(value), nil
}
