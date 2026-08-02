#import <Cocoa/Cocoa.h>

extern void doraIslandOnEvent(int kind, long long value);
extern void doraIslandOnScreen(double x, double y, double width, double height,
                               double visibleX, double visibleY, double visibleWidth, double visibleHeight,
                               double safeTop);

@interface DoraIslandDocumentView : NSView
@end

@implementation DoraIslandDocumentView
- (BOOL)isFlipped { return YES; }
@end

@interface DoraIslandContentView : NSView
@property(nonatomic, strong) NSTrackingArea *doraTrackingArea;
@end

@implementation DoraIslandContentView
- (void)updateTrackingAreas {
    [super updateTrackingAreas];
    if (self.doraTrackingArea != nil) {
        [self removeTrackingArea:self.doraTrackingArea];
    }
    self.doraTrackingArea = [[NSTrackingArea alloc]
        initWithRect:NSZeroRect
        options:NSTrackingMouseEnteredAndExited | NSTrackingActiveAlways | NSTrackingInVisibleRect
        owner:self userInfo:nil];
    [self addTrackingArea:self.doraTrackingArea];
}
- (void)mouseEntered:(NSEvent *)event { doraIslandOnEvent(1, 0); }
- (void)mouseExited:(NSEvent *)event { doraIslandOnEvent(2, 0); }
@end

@interface DoraIslandPanel : NSPanel
@property(nonatomic, strong) DoraIslandContentView *islandContent;
@property(nonatomic) BOOL hasPresented;
@end

@implementation DoraIslandPanel
- (BOOL)canBecomeKeyWindow { return NO; }
- (BOOL)canBecomeMainWindow { return NO; }
@end

@interface DoraIslandPanel (Actions)
- (void)doraRefresh:(id)sender;
- (void)doraOpen:(id)sender;
- (void)doraQuit:(id)sender;
- (void)doraSession:(NSButton *)sender;
@end

static DoraIslandPanel *doraPanel;
static id doraScreenObserver;
static long long doraLastScrolledRequestID;

static NSColor *doraColor(CGFloat red, CGFloat green, CGFloat blue, CGFloat alpha) {
    return [NSColor colorWithSRGBRed:red green:green blue:blue alpha:alpha];
}

static NSTextField *doraLabel(NSString *text, CGFloat size, NSFontWeight weight, NSColor *color) {
    NSTextField *label = [NSTextField labelWithString:text ?: @""];
    label.font = [NSFont systemFontOfSize:size weight:weight];
    label.textColor = color;
    label.lineBreakMode = NSLineBreakByTruncatingTail;
    label.maximumNumberOfLines = 1;
    return label;
}

static NSButton *doraButton(NSString *title, NSString *tooltip, SEL action) {
    NSButton *button = [NSButton buttonWithTitle:title target:doraPanel action:action];
    button.bezelStyle = NSBezelStyleTexturedRounded;
    button.controlSize = NSControlSizeSmall;
    button.toolTip = tooltip;
    return button;
}

static void doraAddLabel(NSView *view, NSString *text, NSRect frame, CGFloat size, NSFontWeight weight, NSColor *color) {
    NSTextField *label = doraLabel(text, size, weight, color);
    label.frame = frame;
    [view addSubview:label];
}

static void doraSendScreen(void) {
    // screens 首项始终是当前承载菜单栏的主屏，不能沿用 panel 的旧 screen。
    NSScreen *screen = NSScreen.screens.firstObject ?: NSScreen.mainScreen;
    if (screen == nil) return;
    NSEdgeInsets safe = NSEdgeInsetsMake(0, 0, 0, 0);
    if (@available(macOS 12.0, *)) {
        safe = screen.safeAreaInsets;
    }
    NSRect frame = screen.frame;
    NSRect visible = screen.visibleFrame;
    doraIslandOnScreen(frame.origin.x, frame.origin.y, frame.size.width, frame.size.height,
                       visible.origin.x, visible.origin.y, visible.size.width, visible.size.height,
                       safe.top);
}

@implementation DoraIslandPanel (Actions)
- (void)doraRefresh:(id)sender { doraIslandOnEvent(3, 0); }
- (void)doraOpen:(id)sender { doraIslandOnEvent(4, 0); }
- (void)doraQuit:(id)sender { doraIslandOnEvent(5, 0); }
- (void)doraSession:(NSButton *)sender { doraIslandOnEvent(6, (long long)sender.tag); }
@end

static void doraBuildCompact(NSDictionary *view, DoraIslandContentView *content) {
    CGFloat width = content.bounds.size.width;
    NSInteger waiting = [view[@"waitingCount"] integerValue];
    NSInteger running = [view[@"runningCount"] integerValue];
    NSString *operationStatus = view[@"operationStatus"] ?: @"";
    BOOL hasOperationStatus = operationStatus.length > 0;
    BOOL operationError = [view[@"operationError"] boolValue];
    NSColor *accent = hasOperationStatus ?
                       (operationError ? doraColor(1.0, 0.34, 0.36, 1.0) : doraColor(0.27, 0.69, 0.48, 1.0)) :
                       (waiting > 0 ? doraColor(1.0, 0.25, 0.30, 1.0) :
                       (running > 0 ? doraColor(0.24, 0.62, 1.0, 1.0) : doraColor(0.55, 0.57, 0.62, 1.0)));
    NSView *dot = [[NSView alloc] initWithFrame:NSMakeRect(15, 16, 8, 8)];
    dot.wantsLayer = YES;
    dot.layer.cornerRadius = 4;
    dot.layer.backgroundColor = accent.CGColor;
    [content addSubview:dot];
    NSString *summary = hasOperationStatus ? operationStatus : view[@"compactSummary"];
    doraAddLabel(content, summary, NSMakeRect(31, 8, width - 130, 24), 13, NSFontWeightSemibold, NSColor.whiteColor);
    NSTextField *tokens = doraLabel(view[@"compactTokens"], 12, NSFontWeightMedium, doraColor(0.72, 0.74, 0.78, 1.0));
    tokens.alignment = NSTextAlignmentRight;
    tokens.frame = NSMakeRect(width - 96, 8, 80, 24);
    tokens.hidden = hasOperationStatus;
    [content addSubview:tokens];
}

static CGFloat doraAddSessionSection(NSArray *sessions, NSString *state, NSString *title,
                                     DoraIslandDocumentView *document, CGFloat y, CGFloat width,
                                     NSInteger highlightID, NSView **highlightView) {
    NSMutableArray *filtered = [NSMutableArray array];
    for (NSDictionary *session in sessions) {
        if ([session[@"state"] isEqualToString:state]) [filtered addObject:session];
    }
    if (filtered.count == 0) return y;
    doraAddLabel(document, [NSString stringWithFormat:@"%@  %lu", title, (unsigned long)filtered.count],
                 NSMakeRect(4, y, width - 8, 22), 11, NSFontWeightSemibold,
                 [state isEqualToString:@"waiting"] ? doraColor(1.0, 0.38, 0.40, 1.0) : doraColor(0.38, 0.68, 1.0, 1.0));
    y += 26;
    for (NSDictionary *session in filtered) {
        NSInteger sessionID = [session[@"id"] longLongValue];
        BOOL highlighted = sessionID == highlightID || [session[@"highlight"] boolValue];
        NSView *row = [[NSView alloc] initWithFrame:NSMakeRect(0, y, width, 58)];
        row.wantsLayer = YES;
        row.layer.cornerRadius = 11;
        row.layer.backgroundColor = (highlighted ? doraColor(0.40, 0.16, 0.18, 0.94) : doraColor(0.11, 0.12, 0.15, 0.96)).CGColor;
        NSView *dot = [[NSView alloc] initWithFrame:NSMakeRect(10, 39, 7, 7)];
        dot.wantsLayer = YES;
        dot.layer.cornerRadius = 3.5;
        dot.layer.backgroundColor = ([state isEqualToString:@"waiting"] ? doraColor(1.0, 0.28, 0.31, 1.0) : doraColor(0.27, 0.63, 1.0, 1.0)).CGColor;
        [row addSubview:dot];
        doraAddLabel(row, session[@"title"], NSMakeRect(24, 31, width * 0.52, 18), 12.5, NSFontWeightSemibold, NSColor.whiteColor);
        doraAddLabel(row, session[@"subtitle"], NSMakeRect(24, 10, width - 38, 18), 11, NSFontWeightRegular, doraColor(0.76, 0.77, 0.81, 1.0));
        NSTextField *meta = doraLabel(session[@"meta"], 9.5, NSFontWeightRegular, doraColor(0.54, 0.57, 0.63, 1.0));
        meta.alignment = NSTextAlignmentRight;
        meta.frame = NSMakeRect(width * 0.48, 32, width * 0.50 - 14, 16);
        [row addSubview:meta];
        NSButton *click = [[NSButton alloc] initWithFrame:row.bounds];
        click.title = @"";
        click.bordered = NO;
        click.transparent = YES;
        click.target = doraPanel;
        click.action = @selector(doraSession:);
        click.tag = sessionID;
        click.toolTip = @"跳转到对应 Codex 会话";
        [row addSubview:click];
        [document addSubview:row];
        if (highlighted) *highlightView = row;
        y += 64;
    }
    return y;
}

static void doraBuildExpanded(NSDictionary *view, DoraIslandContentView *content, CGFloat oldScrollY) {
    CGFloat width = content.bounds.size.width;
    CGFloat height = content.bounds.size.height;
    NSInteger waiting = [view[@"waitingCount"] integerValue];
    NSInteger running = [view[@"runningCount"] integerValue];
    doraAddLabel(content, @"Dora", NSMakeRect(18, height - 37, 80, 22), 16, NSFontWeightBold, NSColor.whiteColor);
    NSString *counts = [NSString stringWithFormat:@"%ld 等待  ·  %ld 运行", (long)waiting, (long)running];
    NSTextField *countLabel = doraLabel(counts, 11, NSFontWeightMedium,
        waiting > 0 ? doraColor(1.0, 0.39, 0.42, 1.0) : doraColor(0.42, 0.70, 1.0, 1.0));
    countLabel.alignment = NSTextAlignmentRight;
    countLabel.frame = NSMakeRect(width - 210, height - 35, 190, 20);
    [content addSubview:countLabel];
    NSArray *tokens = @[view[@"today"] ?: @"", view[@"sevenDays"] ?: @"", view[@"allTime"] ?: @""];
    CGFloat tokenWidth = (width - 36) / 3;
    for (NSInteger index = 0; index < 3; index++) {
        doraAddLabel(content, tokens[index], NSMakeRect(18 + tokenWidth * index, height - 67, tokenWidth - 8, 20), 11.5,
                     NSFontWeightMedium, doraColor(0.86, 0.87, 0.90, 1.0));
    }
    doraAddLabel(content, view[@"fiveHour"], NSMakeRect(18, height - 94, width - 36, 18), 10.5, NSFontWeightRegular, doraColor(0.68, 0.70, 0.75, 1.0));
    doraAddLabel(content, view[@"sevenDay"], NSMakeRect(18, height - 115, width - 36, 18), 10.5, NSFontWeightRegular, doraColor(0.68, 0.70, 0.75, 1.0));

    CGFloat scrollHeight = MAX(48, height - 176);
    NSScrollView *scroll = [[NSScrollView alloc] initWithFrame:NSMakeRect(16, 48, width - 32, scrollHeight)];
    scroll.drawsBackground = NO;
    scroll.hasVerticalScroller = YES;
    scroll.autohidesScrollers = YES;
    NSArray *sessions = view[@"sessions"] ?: @[];
    NSInteger highlightID = [view[@"highlightSessionId"] longLongValue];
    long long highlightRequestID = [view[@"highlightRequestId"] longLongValue];
    CGFloat documentWidth = width - 38;
    CGFloat documentHeight = 8;
    for (NSString *state in @[@"waiting", @"running"]) {
        NSUInteger count = 0;
        for (NSDictionary *session in sessions) if ([session[@"state"] isEqualToString:state]) count++;
        if (count > 0) documentHeight += 26 + count * 64;
    }
    documentHeight = MAX(documentHeight, scrollHeight);
    DoraIslandDocumentView *document = [[DoraIslandDocumentView alloc] initWithFrame:NSMakeRect(0, 0, documentWidth, documentHeight)];
    NSView *highlightView = nil;
    CGFloat y = 4;
    y = doraAddSessionSection(sessions, @"waiting", @"需要处理", document, y, documentWidth, highlightID, &highlightView);
    y = doraAddSessionSection(sessions, @"running", @"运行中", document, y, documentWidth, highlightID, &highlightView);
    if (sessions.count == 0) {
        doraAddLabel(document, @"当前没有活跃的 Codex 会话", NSMakeRect(6, 12, documentWidth - 12, 22), 11,
                     NSFontWeightRegular, doraColor(0.50, 0.52, 0.57, 1.0));
    }
    scroll.documentView = document;
    [content addSubview:scroll];
    if (highlightView != nil && highlightRequestID > 0 && highlightRequestID != doraLastScrolledRequestID) {
        [highlightView scrollRectToVisible:highlightView.bounds];
        doraLastScrolledRequestID = highlightRequestID;
    } else {
        CGFloat maximum = MAX(0, documentHeight - scrollHeight);
        [[scroll contentView] scrollToPoint:NSMakePoint(0, MIN(MAX(0, oldScrollY), maximum))];
        [scroll reflectScrolledClipView:scroll.contentView];
    }

    doraAddLabel(content, view[@"status"], NSMakeRect(18, 15, width - 270, 20), 10, NSFontWeightRegular, doraColor(0.55, 0.58, 0.64, 1.0));
    NSButton *refresh = doraButton([view[@"refreshing"] boolValue] ? @"刷新中" : @"刷新", @"重新扫描 token 并刷新配额", @selector(doraRefresh:));
    refresh.enabled = ![view[@"refreshing"] boolValue];
    refresh.frame = NSMakeRect(width - 246, 11, 66, 26);
    [content addSubview:refresh];
    NSButton *open = doraButton(@"仪表盘", @"使用默认浏览器打开 Dora", @selector(doraOpen:));
    open.frame = NSMakeRect(width - 174, 11, 76, 26);
    [content addSubview:open];
    NSButton *quit = doraButton(@"退出", @"退出 Dora", @selector(doraQuit:));
    quit.frame = NSMakeRect(width - 90, 11, 72, 26);
    [content addSubview:quit];
}

static void doraApplyView(NSDictionary *view) {
    if (doraPanel == nil) return;
    if ([view[@"highlightRequestId"] longLongValue] == 0) doraLastScrolledRequestID = 0;
    NSDictionary *layout = view[@"layout"];
    NSDictionary *frameValue = layout[@"frame"];
    NSRect frame = NSMakeRect([frameValue[@"x"] doubleValue], [frameValue[@"y"] doubleValue],
                              [frameValue[@"width"] doubleValue], [frameValue[@"height"] doubleValue]);
    CGFloat oldScrollY = 0;
    for (NSView *subview in doraPanel.islandContent.subviews) {
        if ([subview isKindOfClass:NSScrollView.class]) {
            oldScrollY = ((NSScrollView *)subview).documentVisibleRect.origin.y;
            break;
        }
    }
    void (^changes)(void) = ^{ [doraPanel setFrame:frame display:YES]; };
    if (doraPanel.hasPresented) {
        [NSAnimationContext runAnimationGroup:^(NSAnimationContext *context) {
            context.duration = 0.19;
            [[doraPanel animator] setFrame:frame display:YES];
        } completionHandler:nil];
    } else {
        changes();
        doraPanel.hasPresented = YES;
    }
    doraPanel.islandContent.frame = NSMakeRect(0, 0, frame.size.width, frame.size.height);
    for (NSView *subview in doraPanel.islandContent.subviews.copy) [subview removeFromSuperview];
    if ([view[@"expanded"] boolValue]) {
        doraBuildExpanded(view, doraPanel.islandContent, oldScrollY);
    } else {
        doraBuildCompact(view, doraPanel.islandContent);
    }
    [doraPanel orderFrontRegardless];
}

void doraIslandStart(void) {
    [NSApplication sharedApplication];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    NSRect initial = NSMakeRect(0, 0, 420, 40);
    doraPanel = [[DoraIslandPanel alloc]
        initWithContentRect:initial
        styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
        backing:NSBackingStoreBuffered defer:NO];
    doraPanel.opaque = NO;
    doraPanel.backgroundColor = NSColor.clearColor;
    doraPanel.hasShadow = YES;
    doraPanel.hidesOnDeactivate = NO;
    doraPanel.level = NSStatusWindowLevel;
    doraPanel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
                                   NSWindowCollectionBehaviorFullScreenAuxiliary |
                                   NSWindowCollectionBehaviorStationary;
    doraPanel.becomesKeyOnlyIfNeeded = YES;
    doraPanel.islandContent = [[DoraIslandContentView alloc] initWithFrame:initial];
    doraPanel.islandContent.wantsLayer = YES;
    doraPanel.islandContent.layer.backgroundColor = doraColor(0.035, 0.038, 0.047, 0.98).CGColor;
    doraPanel.islandContent.layer.cornerRadius = 20;
    doraPanel.islandContent.layer.masksToBounds = YES;
    doraPanel.contentView = doraPanel.islandContent;
    doraScreenObserver = [NSNotificationCenter.defaultCenter
        addObserverForName:NSApplicationDidChangeScreenParametersNotification object:nil queue:NSOperationQueue.mainQueue
        usingBlock:^(NSNotification *note) { doraSendScreen(); }];
    [doraPanel orderFrontRegardless];
    doraSendScreen();
    [NSApp run];
}

void doraIslandPresent(const char *payload) {
    if (payload == NULL) return;
    NSString *json = [NSString stringWithUTF8String:payload];
    dispatch_async(dispatch_get_main_queue(), ^{
        NSData *data = [json dataUsingEncoding:NSUTF8StringEncoding];
        NSDictionary *view = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
        if ([view isKindOfClass:NSDictionary.class]) doraApplyView(view);
    });
}

void doraIslandPlayAttentionSound(void) {
    dispatch_async(dispatch_get_main_queue(), ^{ [[NSSound soundNamed:@"Glass"] play]; });
}

void doraIslandStop(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (doraScreenObserver != nil) {
            [NSNotificationCenter.defaultCenter removeObserver:doraScreenObserver];
            doraScreenObserver = nil;
        }
        [doraPanel close];
        doraPanel = nil;
        [NSApp stop:nil];
        NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint
            modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0];
        [NSApp postEvent:wake atStart:NO];
    });
}
