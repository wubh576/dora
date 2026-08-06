#import <Cocoa/Cocoa.h>

@interface DoraIconButton : NSButton
- (void)setLoading:(BOOL)loading;
@end

@interface DoraIslandContentView : NSView
- (instancetype)initWithFrame:(NSRect)frame actionTarget:(id)actionTarget;
@end

@interface DoraIslandPanel : NSPanel
@end

static NSMutableArray<NSNumber *> *doraEventKinds;
static NSButton *doraObservedRefreshButton;
static NSInteger doraRefreshActionCount;
static BOOL doraPressedDuringRefreshAction;
static CGFloat doraPressedBackgroundAlpha;
static NSInteger doraNextEventNumber;

void doraIslandOnEvent(int kind, long long value) {
    (void)value;
    [doraEventKinds addObject:@(kind)];
    if (kind != 3) return;
    doraRefreshActionCount++;
    doraPressedDuringRefreshAction =
        [[[doraObservedRefreshButton valueForKey:@"doraPressed"] description] boolValue];
    doraPressedBackgroundAlpha = CGColorGetAlpha(doraObservedRefreshButton.layer.backgroundColor);
}

void doraIslandOnScreen(double x, double y, double width, double height,
                        double visibleX, double visibleY, double visibleWidth, double visibleHeight,
                        double safeTop, double menuBarThickness, double notchWidth) {
    (void)x;
    (void)y;
    (void)width;
    (void)height;
    (void)visibleX;
    (void)visibleY;
    (void)visibleWidth;
    (void)visibleHeight;
    (void)safeTop;
    (void)menuBarThickness;
    (void)notchWidth;
}

static int doraAssert(BOOL condition, const char *message) {
    if (condition) return 0;
    fprintf(stderr, "%s\n", message);
    return 1;
}

static BOOL doraRectEqual(NSRect actual, NSRect expected) {
    return NSEqualRects(actual, expected);
}

static NSEvent *doraMouseEvent(DoraIslandPanel *panel, NSEventType type, NSPoint location) {
    doraNextEventNumber++;
    return [NSEvent mouseEventWithType:type location:location modifierFlags:0
        timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:panel.windowNumber
        context:nil eventNumber:doraNextEventNumber clickCount:1 pressure:type == NSEventTypeLeftMouseDown ? 1 : 0];
}

static void doraClick(DoraIslandPanel *panel, NSPoint location) {
    NSEvent *mouseUp = doraMouseEvent(panel, NSEventTypeLeftMouseUp, location);
    [NSApp postEvent:mouseUp atStart:YES];
    [panel sendEvent:doraMouseEvent(panel, NSEventTypeLeftMouseDown, location)];
}

int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        DoraIslandPanel *panel = [[DoraIslandPanel alloc]
            initWithContentRect:NSMakeRect(0, 0, 760, 244)
            styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
            backing:NSBackingStoreBuffered defer:NO];
        DoraIslandContentView *content = [[DoraIslandContentView alloc]
            initWithFrame:NSMakeRect(0, 0, 760, 244) actionTarget:panel];
        panel.contentView = content;
        [content setNeedsLayout:YES];
        [content layoutSubtreeIfNeeded];

        NSView *expandedView = [content valueForKey:@"expandedView"];
        expandedView.hidden = NO;
        NSArray<DoraIconButton *> *buttons = @[
            [content valueForKey:@"refreshButton"],
            [content valueForKey:@"openButton"],
            [content valueForKey:@"settingsButton"],
            [content valueForKey:@"quitButton"],
        ];
        NSArray<NSString *> *names = @[@"刷新数据", @"打开仪表盘", @"打开设置", @"退出 Dora"];
        NSArray<NSString *> *actions = @[@"doraRefresh:", @"doraOpen:", @"doraSettings:", @"doraQuit:"];
        NSArray<NSValue *> *frames = @[
            [NSValue valueWithRect:NSMakeRect(612, 204, 28, 28)],
            [NSValue valueWithRect:NSMakeRect(646, 204, 28, 28)],
            [NSValue valueWithRect:NSMakeRect(680, 204, 28, 28)],
            [NSValue valueWithRect:NSMakeRect(714, 204, 28, 28)],
        ];
        NSUInteger previousIndex = NSNotFound;
        for (NSUInteger index = 0; index < buttons.count; index++) {
            DoraIconButton *button = buttons[index];
            NSString *name = names[index];
            if (doraAssert(button.image != nil, "SF Symbol image was not created")) return 1;
            if (doraAssert([button.toolTip isEqualToString:name], "icon button tooltip is wrong")) return 1;
            if (doraAssert([[button accessibilityLabel] isEqualToString:name],
                           "icon button accessibility label is wrong")) return 1;
            if (doraAssert(button.target == panel, "icon button target is not the real panel")) return 1;
            if (doraAssert(button.action == NSSelectorFromString(actions[index]),
                           "icon button action is wrong")) return 1;
            if (doraAssert(doraRectEqual(button.frame, frames[index].rectValue),
                           "icon button frame is wrong")) return 1;
            NSUInteger subviewIndex = [expandedView.subviews indexOfObjectIdenticalTo:button];
            if (doraAssert(subviewIndex != NSNotFound &&
                           (previousIndex == NSNotFound || subviewIndex > previousIndex),
                           "icon buttons are not ordered refresh, dashboard, settings, quit")) return 1;
            previousIndex = subviewIndex;
        }

        NSArray<NSTextField *> *tokenLabels = [content valueForKey:@"tokenLabels"];
        if (doraAssert(doraRectEqual(tokenLabels[0].frame, NSMakeRect(18, 177, 173, 20)) &&
                       doraRectEqual(tokenLabels[3].frame, NSMakeRect(561, 177, 173, 20)),
                       "token frames changed while moving the toolbar")) return 1;
        if (doraAssert(doraRectEqual([[content valueForKey:@"fiveHourLabel"] frame], NSMakeRect(18, 150, 724, 18)) &&
                       doraRectEqual([[content valueForKey:@"sevenDayLabel"] frame], NSMakeRect(18, 129, 724, 18)),
                       "quota frames changed while moving the toolbar")) return 1;
        if (doraAssert(doraRectEqual([[content valueForKey:@"sessionScroll"] frame], NSMakeRect(16, 48, 728, 68)),
                       "session scroll frame changed while moving the toolbar")) return 1;
        if (doraAssert(doraRectEqual([[content valueForKey:@"countLabel"] frame], NSMakeRect(18, 15, 160, 20)) &&
                       doraRectEqual([[content valueForKey:@"statusLabel"] frame], NSMakeRect(190, 15, 552, 20)),
                       "footer labels are not fixed without overlap")) return 1;

        DoraIconButton *refresh = buttons[0];
        doraObservedRefreshButton = refresh;
        doraEventKinds = [NSMutableArray array];
        [panel orderFrontRegardless];
        [refresh updateTrackingAreas];
        NSPoint refreshCenter = NSMakePoint(NSMidX(refresh.frame), NSMidY(refresh.frame));

        NSEvent *hoverEvent = [NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint
            modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0];
        [refresh mouseEntered:hoverEvent];
        CGFloat hoverAlpha = CGColorGetAlpha(refresh.layer.backgroundColor);
        if (doraAssert(hoverAlpha > 0, "icon button hover background was not shown")) return 1;
        [refresh mouseExited:hoverEvent];
        if (doraAssert(CGColorGetAlpha(refresh.layer.backgroundColor) == 0,
                       "icon button hover background was not cleared")) return 1;

        doraClick(panel, refreshCenter);
        if (doraAssert(doraRefreshActionCount == 1 &&
                       [doraEventKinds isEqualToArray:@[@7, @3, @8]],
                       "real panel click did not produce one refresh bridge action")) return 1;
        if (doraAssert(doraPressedDuringRefreshAction && doraPressedBackgroundAlpha > hoverAlpha,
                       "real panel click did not preserve pressed feedback during action")) return 1;

        [refresh setLoading:YES];
        NSTrackingArea *loadingTrackingArea = [refresh valueForKey:@"doraTrackingArea"];
        [refresh setLoading:YES];
        NSProgressIndicator *progress = nil;
        for (NSView *view in refresh.subviews) {
            if ([view isKindOfClass:NSProgressIndicator.class]) progress = (NSProgressIndicator *)view;
        }
        if (doraAssert(refresh.enabled && refresh.image == nil && progress != nil && !progress.hidden,
                       "refresh loading state disabled the button or hid progress")) return 1;
        if (doraAssert([refresh valueForKey:@"doraTrackingArea"] == loadingTrackingArea,
                       "idempotent loading update replaced the tracking area")) return 1;
        if (doraAssert([refresh hitTest:refreshCenter] == refresh,
                       "refresh progress indicator intercepted panel hit-testing")) return 1;
        [doraEventKinds removeAllObjects];
        for (NSInteger click = 0; click < 5; click++) doraClick(panel, refreshCenter);
        if (doraAssert(doraRefreshActionCount == 1 && doraEventKinds.count == 10,
                       "repeated loading clicks produced another bridge action")) return 1;
        for (NSUInteger index = 0; index < doraEventKinds.count; index += 2) {
            if (doraAssert([doraEventKinds[index] isEqualToNumber:@7] &&
                           [doraEventKinds[index + 1] isEqualToNumber:@8],
                           "loading click bypassed the real panel event boundary")) return 1;
        }

        [refresh setLoading:NO];
        NSTrackingArea *normalTrackingArea = [refresh valueForKey:@"doraTrackingArea"];
        [refresh setLoading:NO];
        if (doraAssert(refresh.enabled && refresh.image != nil && progress.hidden,
                       "refresh loading state did not restore the icon")) return 1;
        if (doraAssert([refresh valueForKey:@"doraTrackingArea"] == normalTrackingArea,
                       "loading recovery replaced the tracking area")) return 1;
        [doraEventKinds removeAllObjects];
        doraClick(panel, refreshCenter);
        if (doraAssert(doraRefreshActionCount == 2 &&
                       [doraEventKinds isEqualToArray:@[@7, @3, @8]],
                       "refresh did not accept a new click after loading completed")) return 1;

        [panel orderOut:nil];
        [panel close];
        doraObservedRefreshButton = nil;
        doraEventKinds = nil;
    }
    return 0;
}
