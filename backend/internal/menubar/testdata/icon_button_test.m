#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

@interface DoraIconButton : NSButton
- (void)setLoading:(BOOL)loading;
@end

@interface DoraIslandContentView : NSView
- (instancetype)initWithFrame:(NSRect)frame actionTarget:(id)actionTarget;
@end

@interface DoraToolbarActionTarget : NSObject
@property(nonatomic) BOOL refreshActionCalled;
@property(nonatomic) BOOL refreshPressedDuringAction;
@property(nonatomic) CGFloat refreshPressedBackgroundAlpha;
@end

@implementation DoraToolbarActionTarget
- (void)doraRefresh:(id)sender {
    self.refreshActionCalled = YES;
    self.refreshPressedDuringAction = [[[sender valueForKey:@"doraPressed"] description] boolValue];
    self.refreshPressedBackgroundAlpha = CGColorGetAlpha(((NSButton *)sender).layer.backgroundColor);
}
- (void)doraOpen:(id)sender { (void)sender; }
- (void)doraSettings:(id)sender { (void)sender; }
- (void)doraQuit:(id)sender { (void)sender; }
@end

@interface NSButton (DoraIconButtonTest)
- (void)doraTestMouseDown:(NSEvent *)event;
@end

@implementation NSButton (DoraIconButtonTest)
- (void)doraTestMouseDown:(NSEvent *)event {
    (void)event;
    [NSApp sendAction:self.action to:self.target from:self];
}
@end

void doraIslandOnEvent(int kind, long long value) {
    (void)kind;
    (void)value;
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

int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        DoraToolbarActionTarget *target = [[DoraToolbarActionTarget alloc] init];
        DoraIslandContentView *content = [[DoraIslandContentView alloc]
            initWithFrame:NSMakeRect(0, 0, 760, 244) actionTarget:target];
        [content setNeedsLayout:YES];
        [content layoutSubtreeIfNeeded];

        NSView *expandedView = [content valueForKey:@"expandedView"];
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
            if (doraAssert(button.target == target, "icon button target is wrong")) return 1;
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
        [refresh setLoading:YES];
        NSProgressIndicator *progress = nil;
        for (NSView *view in refresh.subviews) {
            if ([view isKindOfClass:NSProgressIndicator.class]) progress = (NSProgressIndicator *)view;
        }
        if (doraAssert(!refresh.enabled && refresh.image == nil && progress != nil && !progress.hidden,
                       "refresh loading state did not disable the icon and show progress")) return 1;
        [refresh setLoading:NO];
        if (doraAssert(refresh.enabled && refresh.image != nil && progress.hidden,
                       "refresh loading state did not restore the icon")) return 1;

        NSEvent *event = [NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint
            modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0];
        [refresh mouseEntered:event];
        CGFloat hoverAlpha = CGColorGetAlpha(refresh.layer.backgroundColor);
        if (doraAssert(hoverAlpha > 0,
                       "icon button hover background was not shown")) return 1;
        [refresh mouseExited:event];
        if (doraAssert(CGColorGetAlpha(refresh.layer.backgroundColor) == 0,
                       "icon button hover background was not cleared")) return 1;

        Method mouseDownMethod = class_getInstanceMethod(NSButton.class, @selector(mouseDown:));
        Method testMouseDownMethod = class_getInstanceMethod(NSButton.class, @selector(doraTestMouseDown:));
        method_exchangeImplementations(mouseDownMethod, testMouseDownMethod);
        [refresh mouseDown:event];
        method_exchangeImplementations(mouseDownMethod, testMouseDownMethod);
        if (doraAssert(target.refreshActionCalled && target.refreshPressedDuringAction &&
                       target.refreshPressedBackgroundAlpha > hoverAlpha,
                       "mouseDown did not expose pressed feedback while AppKit dispatched the action")) return 1;
        if (doraAssert(![[refresh valueForKey:@"doraPressed"] boolValue] &&
                       CGColorGetAlpha(refresh.layer.backgroundColor) == 0,
                       "pressed feedback was not reset after mouseDown returned")) return 1;
    }
    return 0;
}
