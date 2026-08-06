#import "settings_window_darwin.h"

@interface DoraSettingsWindowController : NSWindowController
- (void)showSettingsWindow;
@end

@implementation DoraSettingsWindowController

- (instancetype)init {
    NSRect frame = NSMakeRect(0, 0, 640, 420);
    NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                              NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
    NSWindow *window = [[NSWindow alloc] initWithContentRect:frame styleMask:style
        backing:NSBackingStoreBuffered defer:NO];
    window.title = @"Dora 设置";
    window.minSize = NSMakeSize(480, 320);
    window.releasedWhenClosed = NO;
    [window center];

    NSView *content = [[NSView alloc] initWithFrame:frame];
    NSTextField *title = [NSTextField labelWithString:@"Dora 设置"];
    title.font = [NSFont systemFontOfSize:22 weight:NSFontWeightSemibold];
    title.textColor = NSColor.labelColor;
    title.alignment = NSTextAlignmentCenter;
    title.translatesAutoresizingMaskIntoConstraints = NO;
    NSTextField *message = [NSTextField labelWithString:@"设置项将在后续加入"];
    message.font = [NSFont systemFontOfSize:13 weight:NSFontWeightRegular];
    message.textColor = NSColor.secondaryLabelColor;
    message.alignment = NSTextAlignmentCenter;
    message.translatesAutoresizingMaskIntoConstraints = NO;
    [content addSubview:title];
    [content addSubview:message];
    [NSLayoutConstraint activateConstraints:@[
        [title.centerXAnchor constraintEqualToAnchor:content.centerXAnchor],
        [title.bottomAnchor constraintEqualToAnchor:content.centerYAnchor constant:-6],
        [message.centerXAnchor constraintEqualToAnchor:content.centerXAnchor],
        [message.topAnchor constraintEqualToAnchor:title.bottomAnchor constant:10],
    ]];
    window.contentView = content;

    self = [super initWithWindow:window];
    return self;
}

- (void)showSettingsWindow {
    [NSApp activateIgnoringOtherApps:YES];
    [self showWindow:nil];
    [self.window makeKeyAndOrderFront:nil];
}

@end

static DoraSettingsWindowController *doraSettingsController;

void DoraShowSettingsWindow(void) {
    if (doraSettingsController == nil) {
        doraSettingsController = [[DoraSettingsWindowController alloc] init];
    }
    [doraSettingsController showSettingsWindow];
}

void DoraCloseSettingsWindow(void) {
    [doraSettingsController close];
    doraSettingsController = nil;
}
