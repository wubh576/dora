#import <Cocoa/Cocoa.h>
#import "settings_window_darwin.h"

static int doraAssert(BOOL condition, const char *message) {
    if (condition) return 0;
    fprintf(stderr, "%s\n", message);
    return 1;
}

static NSUInteger doraSettingsWindowCount(void) {
    NSUInteger count = 0;
    for (NSWindow *window in NSApp.windows) {
        if ([window.title isEqualToString:@"Dora 设置"]) count++;
    }
    return count;
}

int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

        DoraShowSettingsWindow();
        NSWindow *originalWindow = nil;
        for (NSWindow *window in NSApp.windows) {
            if ([window.title isEqualToString:@"Dora 设置"]) originalWindow = window;
        }
        if (doraAssert(originalWindow != nil, "settings window was not created")) return 1;
        if (doraAssert(originalWindow.isVisible, "settings window was not shown")) return 1;
        if (doraAssert(doraSettingsWindowCount() == 1, "settings window was created more than once")) return 1;

        DoraShowSettingsWindow();
        NSWindow *reopenedWindow = nil;
        for (NSWindow *window in NSApp.windows) {
            if ([window.title isEqualToString:@"Dora 设置"]) reopenedWindow = window;
        }
        if (doraAssert(reopenedWindow == originalWindow, "reopening replaced the settings window")) return 1;
        if (doraAssert(doraSettingsWindowCount() == 1, "reopening duplicated the settings window")) return 1;

        [originalWindow close];
        if (doraAssert(!originalWindow.isVisible, "settings window did not close")) return 1;
        DoraShowSettingsWindow();
        if (doraAssert(originalWindow.isVisible,
                       "closed settings window could not be reused")) return 1;
        if (doraAssert(doraSettingsWindowCount() == 1, "reopened settings window was duplicated")) return 1;

        DoraCloseSettingsWindow();
        if (doraAssert(!originalWindow.isVisible,
                       "settings window remained visible after Dora shutdown cleanup")) return 1;
    }
    return 0;
}
