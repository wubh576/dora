#import <Cocoa/Cocoa.h>

extern void doraIslandOnEvent(int kind, long long value);
extern void doraIslandOnScreen(double x, double y, double width, double height,
                               double visibleX, double visibleY, double visibleWidth, double visibleHeight,
                               double safeTop, double menuBarThickness);

@class DoraIslandPanel;
static DoraIslandPanel *doraPanel;
static id doraScreenObserver;
static NSViewAnimation *doraFrameAnimation;
static NSTimer *doraPointerTimer;
static long long doraLastScrolledRequestID;
static BOOL doraPointerKnown;
static BOOL doraPointerInside;
static const NSTimeInterval doraPointerSampleInterval = 0.05;

static BOOL doraCurrentPointerInside(void);
static void doraPublishPointerState(BOOL inside);
static void doraSamplePointer(void);

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

@interface DoraIslandDocumentView : NSView
@end

@implementation DoraIslandDocumentView
- (BOOL)isFlipped { return YES; }
@end

@interface DoraActionButton : NSButton
@property(nonatomic, strong) NSTrackingArea *doraTrackingArea;
@property(nonatomic) BOOL doraHovered;
@end

@implementation DoraActionButton
- (instancetype)initWithTitle:(NSString *)title toolTip:(NSString *)toolTip action:(SEL)action {
    self = [super initWithFrame:NSZeroRect];
    if (self != nil) {
        self.title = title;
        self.toolTip = toolTip;
        self.target = doraPanel;
        self.action = action;
        self.bordered = NO;
        self.font = [NSFont systemFontOfSize:10.5 weight:NSFontWeightMedium];
        self.contentTintColor = doraColor(0.72, 0.74, 0.79, 1.0);
        self.wantsLayer = YES;
        self.layer.cornerRadius = 7;
        self.layer.borderWidth = 0.7;
        self.layer.borderColor = doraColor(0.25, 0.27, 0.32, 0.8).CGColor;
    }
    return self;
}
- (void)updateTrackingAreas {
    [super updateTrackingAreas];
    if (self.doraTrackingArea != nil) [self removeTrackingArea:self.doraTrackingArea];
    self.doraTrackingArea = [[NSTrackingArea alloc]
        initWithRect:NSZeroRect options:NSTrackingMouseEnteredAndExited | NSTrackingActiveAlways | NSTrackingInVisibleRect
        owner:self userInfo:nil];
    [self addTrackingArea:self.doraTrackingArea];
}
- (void)mouseEntered:(NSEvent *)event {
    self.doraHovered = YES;
    self.layer.backgroundColor = doraColor(0.16, 0.17, 0.21, 0.9).CGColor;
}
- (void)mouseExited:(NSEvent *)event {
    self.doraHovered = NO;
    self.layer.backgroundColor = NSColor.clearColor.CGColor;
}
@end

@interface DoraIslandScrollView : NSScrollView
@property(nonatomic) BOOL doraScrolling;
@end

@implementation DoraIslandScrollView
- (void)scrollWheel:(NSEvent *)event {
    if (!self.doraScrolling) {
        self.doraScrolling = YES;
        doraIslandOnEvent(7, 0);
    }
    [NSObject cancelPreviousPerformRequestsWithTarget:self selector:@selector(doraEndScroll) object:nil];
    [super scrollWheel:event];
    [self performSelector:@selector(doraEndScroll) withObject:nil afterDelay:0.45];
}
- (void)doraEndScroll {
    self.doraScrolling = NO;
    doraIslandOnEvent(8, 0);
}
@end

@interface DoraSessionRowView : NSView
@property(nonatomic, strong) NSView *stateDot;
@property(nonatomic, strong) NSTextField *titleLabel;
@property(nonatomic, strong) NSTextField *subtitleLabel;
@property(nonatomic, strong) NSTextField *metaLabel;
@property(nonatomic, strong) NSButton *clickButton;
@property(nonatomic, strong) NSTrackingArea *doraTrackingArea;
@property(nonatomic) BOOL doraHovered;
@property(nonatomic) BOOL doraHighlighted;
@property(nonatomic) BOOL doraJumpable;
- (void)applySession:(NSDictionary *)session;
@end

@implementation DoraSessionRowView
- (instancetype)initWithFrame:(NSRect)frame {
    self = [super initWithFrame:frame];
    if (self != nil) {
        self.wantsLayer = YES;
        self.layer.cornerRadius = 9;
        self.stateDot = [[NSView alloc] initWithFrame:NSZeroRect];
        self.stateDot.wantsLayer = YES;
        self.stateDot.layer.cornerRadius = 3.5;
        [self addSubview:self.stateDot];
        self.titleLabel = doraLabel(@"", 12, NSFontWeightSemibold, NSColor.whiteColor);
        self.subtitleLabel = doraLabel(@"", 10.5, NSFontWeightRegular, doraColor(0.73, 0.75, 0.79, 1.0));
        self.metaLabel = doraLabel(@"", 9.5, NSFontWeightRegular, doraColor(0.52, 0.55, 0.61, 1.0));
        self.metaLabel.alignment = NSTextAlignmentRight;
        [self addSubview:self.titleLabel];
        [self addSubview:self.subtitleLabel];
        [self addSubview:self.metaLabel];
        self.clickButton = [[NSButton alloc] initWithFrame:NSZeroRect];
        self.clickButton.title = @"";
        self.clickButton.bordered = NO;
        self.clickButton.transparent = YES;
        self.clickButton.target = doraPanel;
        [self addSubview:self.clickButton];
    }
    return self;
}
- (void)layout {
    [super layout];
    CGFloat width = self.bounds.size.width;
    self.stateDot.frame = NSMakeRect(8, 32, 7, 7);
    self.titleLabel.frame = NSMakeRect(23, 25, width * 0.52, 18);
    self.subtitleLabel.frame = NSMakeRect(23, 6, width - 34, 17);
    self.metaLabel.frame = NSMakeRect(width * 0.48, 26, width * 0.50 - 10, 16);
    self.clickButton.frame = self.bounds;
}
- (void)updateTrackingAreas {
    [super updateTrackingAreas];
    if (self.doraTrackingArea != nil) [self removeTrackingArea:self.doraTrackingArea];
    self.doraTrackingArea = [[NSTrackingArea alloc]
        initWithRect:NSZeroRect options:NSTrackingMouseEnteredAndExited | NSTrackingActiveAlways | NSTrackingInVisibleRect
        owner:self userInfo:nil];
    [self addTrackingArea:self.doraTrackingArea];
}
- (void)mouseEntered:(NSEvent *)event { self.doraHovered = YES; [self updateBackground]; }
- (void)mouseExited:(NSEvent *)event { self.doraHovered = NO; [self updateBackground]; }
- (void)updateBackground {
    if (self.doraHighlighted) {
        self.layer.backgroundColor = doraColor(0.30, 0.10, 0.12, 0.72).CGColor;
    } else if (self.doraHovered) {
        self.layer.backgroundColor = doraColor(0.15, 0.16, 0.19, 0.72).CGColor;
    } else {
        self.layer.backgroundColor = NSColor.clearColor.CGColor;
    }
}
- (void)applySession:(NSDictionary *)session {
    NSString *state = session[@"state"] ?: @"running";
    long long sessionID = [session[@"id"] longLongValue];
    self.doraHighlighted = [session[@"highlight"] boolValue];
    self.doraJumpable = [session[@"jumpable"] boolValue];
    self.titleLabel.stringValue = session[@"title"] ?: @"";
    self.subtitleLabel.stringValue = session[@"subtitle"] ?: @"";
    self.metaLabel.stringValue = session[@"meta"] ?: @"";
    NSColor *activeColor = [state isEqualToString:@"waiting"] ?
        doraColor(1.0, 0.30, 0.33, 1.0) : doraColor(0.28, 0.63, 1.0, 1.0);
    self.stateDot.layer.backgroundColor = (self.doraJumpable ? activeColor : doraColor(0.40, 0.42, 0.47, 1.0)).CGColor;
    self.titleLabel.textColor = self.doraJumpable ? NSColor.whiteColor : doraColor(0.58, 0.60, 0.65, 1.0);
    self.clickButton.tag = (NSInteger)sessionID;
    self.clickButton.action = self.doraJumpable ? @selector(doraSession:) : @selector(doraUnavailableSession:);
    self.clickButton.toolTip = self.doraJumpable ? @"跳转到对应 Codex 会话" : (session[@"jumpReason"] ?: @"当前会话无法精确跳转");
    [self updateBackground];
    [self setNeedsLayout:YES];
}
@end

@interface DoraIslandContentView : NSView
@property(nonatomic, strong) NSTrackingArea *doraTrackingArea;
@property(nonatomic, strong) NSView *compactView;
@property(nonatomic, strong) NSTextField *compactTitle;
@property(nonatomic, strong) NSTextField *compactStatus;
@property(nonatomic, strong) NSView *expandedView;
@property(nonatomic, strong) NSTextField *expandedTitle;
@property(nonatomic, strong) NSTextField *countLabel;
@property(nonatomic, strong) NSArray<NSTextField *> *tokenLabels;
@property(nonatomic, strong) NSTextField *fiveHourLabel;
@property(nonatomic, strong) NSTextField *sevenDayLabel;
@property(nonatomic, strong) NSTextField *statusLabel;
@property(nonatomic, strong) DoraActionButton *refreshButton;
@property(nonatomic, strong) DoraActionButton *openButton;
@property(nonatomic, strong) DoraActionButton *quitButton;
@property(nonatomic, strong) DoraIslandScrollView *sessionScroll;
@property(nonatomic, strong) DoraIslandDocumentView *sessionDocument;
@property(nonatomic, strong) NSTextField *waitingHeader;
@property(nonatomic, strong) NSTextField *runningHeader;
@property(nonatomic, strong) NSMutableDictionary<NSNumber *, DoraSessionRowView *> *sessionRows;
@property(nonatomic, copy) NSArray<NSDictionary *> *sessions;
@property(nonatomic) long long highlightRequestID;
- (void)applyView:(NSDictionary *)view;
@end

@implementation DoraIslandContentView
- (instancetype)initWithFrame:(NSRect)frame {
    self = [super initWithFrame:frame];
    if (self != nil) {
        self.wantsLayer = YES;
        self.layer.backgroundColor = doraColor(0.035, 0.038, 0.047, 0.985).CGColor;
        self.layer.cornerRadius = 20;
        self.layer.maskedCorners = kCALayerMinXMinYCorner | kCALayerMaxXMinYCorner;
        self.layer.masksToBounds = YES;
        self.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
        [self buildPersistentViews];
    }
    return self;
}
- (void)buildPersistentViews {
    self.compactView = [[NSView alloc] initWithFrame:self.bounds];
    self.compactTitle = doraLabel(@"Dora", 13, NSFontWeightSemibold, NSColor.whiteColor);
    self.compactStatus = doraLabel(@"0 等待 · 0 运行", 12, NSFontWeightMedium, doraColor(0.42, 0.70, 1.0, 1.0));
    self.compactStatus.alignment = NSTextAlignmentRight;
    [self.compactView addSubview:self.compactTitle];
    [self.compactView addSubview:self.compactStatus];
    [self addSubview:self.compactView];

    self.expandedView = [[NSView alloc] initWithFrame:self.bounds];
    self.expandedView.hidden = YES;
    self.expandedTitle = doraLabel(@"Dora", 16, NSFontWeightBold, NSColor.whiteColor);
    self.countLabel = doraLabel(@"0 等待  ·  0 运行", 11, NSFontWeightMedium, doraColor(0.42, 0.70, 1.0, 1.0));
    self.countLabel.alignment = NSTextAlignmentRight;
    NSMutableArray *tokens = [NSMutableArray arrayWithCapacity:3];
    for (NSInteger index = 0; index < 3; index++) {
        NSTextField *label = doraLabel(@"", 11.5, NSFontWeightMedium, doraColor(0.86, 0.87, 0.90, 1.0));
        [tokens addObject:label];
        [self.expandedView addSubview:label];
    }
    self.tokenLabels = tokens;
    self.fiveHourLabel = doraLabel(@"", 10.5, NSFontWeightRegular, doraColor(0.68, 0.70, 0.75, 1.0));
    self.sevenDayLabel = doraLabel(@"", 10.5, NSFontWeightRegular, doraColor(0.68, 0.70, 0.75, 1.0));
    self.statusLabel = doraLabel(@"", 10, NSFontWeightRegular, doraColor(0.55, 0.58, 0.64, 1.0));
    [self.expandedView addSubview:self.expandedTitle];
    [self.expandedView addSubview:self.countLabel];
    [self.expandedView addSubview:self.fiveHourLabel];
    [self.expandedView addSubview:self.sevenDayLabel];
    [self.expandedView addSubview:self.statusLabel];

    self.sessionScroll = [[DoraIslandScrollView alloc] initWithFrame:NSZeroRect];
    self.sessionScroll.drawsBackground = NO;
    self.sessionScroll.hasVerticalScroller = YES;
    self.sessionScroll.autohidesScrollers = YES;
    self.sessionDocument = [[DoraIslandDocumentView alloc] initWithFrame:NSZeroRect];
    self.waitingHeader = doraLabel(@"", 11, NSFontWeightSemibold, doraColor(1.0, 0.38, 0.40, 1.0));
    self.runningHeader = doraLabel(@"", 11, NSFontWeightSemibold, doraColor(0.38, 0.68, 1.0, 1.0));
    [self.sessionDocument addSubview:self.waitingHeader];
    [self.sessionDocument addSubview:self.runningHeader];
    self.sessionScroll.documentView = self.sessionDocument;
    self.sessionRows = [NSMutableDictionary dictionary];
    [self.expandedView addSubview:self.sessionScroll];

    self.refreshButton = [[DoraActionButton alloc] initWithTitle:@"刷新" toolTip:@"重新扫描 token 并刷新配额" action:@selector(doraRefresh:)];
    self.openButton = [[DoraActionButton alloc] initWithTitle:@"仪表盘" toolTip:@"使用默认浏览器打开 Dora" action:@selector(doraOpen:)];
    self.quitButton = [[DoraActionButton alloc] initWithTitle:@"退出" toolTip:@"退出 Dora" action:@selector(doraQuit:)];
    [self.expandedView addSubview:self.refreshButton];
    [self.expandedView addSubview:self.openButton];
    [self.expandedView addSubview:self.quitButton];
    [self addSubview:self.expandedView];
}
- (void)updateTrackingAreas {
    [super updateTrackingAreas];
    if (self.doraTrackingArea != nil) [self removeTrackingArea:self.doraTrackingArea];
    self.doraTrackingArea = [[NSTrackingArea alloc]
        initWithRect:NSZeroRect options:NSTrackingMouseEnteredAndExited | NSTrackingActiveAlways | NSTrackingInVisibleRect
        owner:self userInfo:nil];
    [self addTrackingArea:self.doraTrackingArea];
}
- (void)mouseEntered:(NSEvent *)event { doraSamplePointer(); }
- (void)mouseExited:(NSEvent *)event { doraSamplePointer(); }
- (void)layout {
    [super layout];
    CGFloat width = self.bounds.size.width;
    CGFloat height = self.bounds.size.height;
    self.compactView.frame = self.bounds;
    CGFloat compactLabelHeight = MIN(24, height);
    CGFloat compactLabelY = (height - compactLabelHeight) / 2;
    self.compactTitle.frame = NSMakeRect(18, compactLabelY, 110, compactLabelHeight);
    self.compactStatus.frame = NSMakeRect(width - 172, compactLabelY, 154, compactLabelHeight);
    self.expandedView.frame = self.bounds;
    self.expandedTitle.frame = NSMakeRect(18, height - 37, 80, 22);
    self.countLabel.frame = NSMakeRect(width - 210, height - 35, 190, 20);
    CGFloat tokenWidth = (width - 36) / 3;
    for (NSInteger index = 0; index < 3; index++) {
        self.tokenLabels[index].frame = NSMakeRect(18 + tokenWidth * index, height - 67, tokenWidth - 8, 20);
    }
    self.fiveHourLabel.frame = NSMakeRect(18, height - 94, width - 36, 18);
    self.sevenDayLabel.frame = NSMakeRect(18, height - 115, width - 36, 18);
    self.sessionScroll.frame = NSMakeRect(16, 48, width - 32, MAX(48, height - 176));
    self.statusLabel.frame = NSMakeRect(18, 15, width - 270, 20);
    self.refreshButton.frame = NSMakeRect(width - 246, 11, 66, 26);
    self.openButton.frame = NSMakeRect(width - 174, 11, 76, 26);
    self.quitButton.frame = NSMakeRect(width - 90, 11, 72, 26);
    [self layoutSessionRows];
}
- (void)layoutSessionRows {
    CGFloat documentWidth = MAX(1, self.sessionScroll.contentSize.width);
    CGFloat scrollHeight = MAX(1, self.sessionScroll.contentSize.height);
    CGFloat y = 4;
    for (NSString *state in @[@"waiting", @"running"]) {
        NSMutableArray<NSDictionary *> *filtered = [NSMutableArray array];
        for (NSDictionary *session in self.sessions) {
            if ([session[@"state"] isEqualToString:state]) [filtered addObject:session];
        }
        NSTextField *header = [state isEqualToString:@"waiting"] ? self.waitingHeader : self.runningHeader;
        header.hidden = filtered.count == 0;
        if (filtered.count == 0) continue;
        NSString *title = [state isEqualToString:@"waiting"] ? @"需要处理" : @"运行中";
        header.stringValue = [NSString stringWithFormat:@"%@  %lu", title, (unsigned long)filtered.count];
        header.frame = NSMakeRect(4, y, documentWidth - 8, 22);
        y += 24;
        for (NSDictionary *session in filtered) {
            NSNumber *key = @([session[@"id"] longLongValue]);
            DoraSessionRowView *row = self.sessionRows[key];
            row.frame = NSMakeRect(0, y, documentWidth, 52);
            [row setNeedsLayout:YES];
            y += 56;
        }
    }
    CGFloat documentHeight = MAX(scrollHeight, y + 4);
    self.sessionDocument.frame = NSMakeRect(0, 0, documentWidth, documentHeight);
}
- (void)applyView:(NSDictionary *)view {
    BOOL expanded = [view[@"expanded"] boolValue];
    self.compactView.hidden = expanded;
    self.expandedView.hidden = !expanded;
    self.compactTitle.stringValue = @"Dora";
    self.compactStatus.stringValue = view[@"compactStatus"] ?: @"0 等待 · 0 运行";
    NSInteger waiting = [view[@"waitingCount"] integerValue];
    NSInteger running = [view[@"runningCount"] integerValue];
    self.compactStatus.textColor = waiting > 0 ? doraColor(1.0, 0.39, 0.42, 1.0) : doraColor(0.42, 0.70, 1.0, 1.0);
    self.countLabel.stringValue = [NSString stringWithFormat:@"%ld 等待  ·  %ld 运行", (long)waiting, (long)running];
    self.countLabel.textColor = waiting > 0 ? doraColor(1.0, 0.39, 0.42, 1.0) : doraColor(0.42, 0.70, 1.0, 1.0);
    self.tokenLabels[0].stringValue = view[@"today"] ?: @"";
    self.tokenLabels[1].stringValue = view[@"sevenDays"] ?: @"";
    self.tokenLabels[2].stringValue = view[@"allTime"] ?: @"";
    self.fiveHourLabel.stringValue = view[@"fiveHour"] ?: @"";
    self.sevenDayLabel.stringValue = view[@"sevenDay"] ?: @"";
    self.statusLabel.stringValue = view[@"status"] ?: @"";
    self.statusLabel.textColor = [view[@"operationError"] boolValue] ? doraColor(1.0, 0.42, 0.44, 1.0) : doraColor(0.55, 0.58, 0.64, 1.0);
    self.refreshButton.title = [view[@"refreshing"] boolValue] ? @"刷新中" : @"刷新";
    self.refreshButton.enabled = ![view[@"refreshing"] boolValue];

    CGFloat oldScrollY = self.sessionScroll.documentVisibleRect.origin.y;
    self.sessions = view[@"sessions"] ?: @[];
    self.highlightRequestID = [view[@"highlightRequestId"] longLongValue];
    if (self.highlightRequestID == 0) doraLastScrolledRequestID = 0;
    NSMutableSet<NSNumber *> *wanted = [NSMutableSet setWithCapacity:self.sessions.count];
    DoraSessionRowView *highlightRow = nil;
    for (NSDictionary *session in self.sessions) {
        NSNumber *key = @([session[@"id"] longLongValue]);
        [wanted addObject:key];
        DoraSessionRowView *row = self.sessionRows[key];
        if (row == nil) {
            row = [[DoraSessionRowView alloc] initWithFrame:NSZeroRect];
            self.sessionRows[key] = row;
            [self.sessionDocument addSubview:row];
        }
        [row applySession:session];
        if ([session[@"highlight"] boolValue]) highlightRow = row;
    }
    for (NSNumber *key in self.sessionRows.allKeys.copy) {
        if (![wanted containsObject:key]) {
            [self.sessionRows[key] removeFromSuperview];
            [self.sessionRows removeObjectForKey:key];
        }
    }
    [self setNeedsLayout:YES];
    [self layoutSubtreeIfNeeded];
    if (highlightRow != nil && self.highlightRequestID > 0 && self.highlightRequestID != doraLastScrolledRequestID) {
        [highlightRow scrollRectToVisible:highlightRow.bounds];
        doraLastScrolledRequestID = self.highlightRequestID;
    } else {
        CGFloat maximum = MAX(0, self.sessionDocument.frame.size.height - self.sessionScroll.contentSize.height);
        [self.sessionScroll.contentView scrollToPoint:NSMakePoint(0, MIN(MAX(0, oldScrollY), maximum))];
        [self.sessionScroll reflectScrolledClipView:self.sessionScroll.contentView];
    }
}
@end

@interface DoraIslandPanel : NSPanel
@property(nonatomic, strong) DoraIslandContentView *islandContent;
@property(nonatomic) BOOL hasPresented;
@property(nonatomic) BOOL hasTargetFrame;
@property(nonatomic) NSRect targetFrame;
@end

@implementation DoraIslandPanel
- (BOOL)canBecomeKeyWindow { return NO; }
- (BOOL)canBecomeMainWindow { return NO; }
- (void)sendEvent:(NSEvent *)event {
    if (event.type == NSEventTypeLeftMouseDown) doraIslandOnEvent(7, 0);
    [super sendEvent:event];
    if (event.type == NSEventTypeLeftMouseUp) doraIslandOnEvent(8, 0);
}
- (void)doraRefresh:(id)sender { doraIslandOnEvent(3, 0); }
- (void)doraOpen:(id)sender { doraIslandOnEvent(4, 0); }
- (void)doraQuit:(id)sender { doraIslandOnEvent(5, 0); }
- (void)doraSession:(NSButton *)sender { doraIslandOnEvent(6, (long long)sender.tag); }
- (void)doraUnavailableSession:(NSButton *)sender { doraIslandOnEvent(9, (long long)sender.tag); }
@end

static BOOL doraCurrentPointerInside(void) {
    if (doraPanel == nil) return NO;
    // 包含屏幕顶边的鼠标热点，避免 NSPointInRect 的 maxY 排他边界误判。
    return NSPointInRect(NSEvent.mouseLocation, NSInsetRect(doraPanel.frame, -1, -1));
}

static void doraPublishPointerState(BOOL inside) {
    if (doraPointerKnown && doraPointerInside == inside) return;
    doraPointerKnown = YES;
    doraPointerInside = inside;
    doraIslandOnEvent(inside ? 1 : 2, 0);
}

static void doraSamplePointer(void) {
    doraPublishPointerState(doraCurrentPointerInside());
}

static void doraSendScreen(void) {
    // screens 首项始终是当前承载菜单栏的主屏，不能沿用 panel 的旧 screen。
    NSScreen *screen = NSScreen.screens.firstObject ?: NSScreen.mainScreen;
    if (screen == nil) return;
    NSEdgeInsets safe = NSEdgeInsetsMake(0, 0, 0, 0);
    if (@available(macOS 12.0, *)) safe = screen.safeAreaInsets;
    NSRect frame = screen.frame;
    NSRect visible = screen.visibleFrame;
    doraIslandOnScreen(frame.origin.x, frame.origin.y, frame.size.width, frame.size.height,
                       visible.origin.x, visible.origin.y, visible.size.width, visible.size.height,
                       safe.top, NSStatusBar.systemStatusBar.thickness);
}

static CGFloat doraCompactHeight(NSScreen *screen) {
    if (screen != nil) {
        CGFloat visibleMenuBarHeight = NSMaxY(screen.frame) - NSMaxY(screen.visibleFrame);
        if (visibleMenuBarHeight > 0) return visibleMenuBarHeight;
        if (@available(macOS 12.0, *)) {
            if (screen.safeAreaInsets.top > 0) return screen.safeAreaInsets.top;
        }
    }
    return MAX(1, NSStatusBar.systemStatusBar.thickness);
}

static void doraApplyView(NSDictionary *view) {
    if (doraPanel == nil) return;
    NSDictionary *frameValue = view[@"layout"][@"frame"];
    NSRect target = NSMakeRect([frameValue[@"x"] doubleValue], [frameValue[@"y"] doubleValue],
                               [frameValue[@"width"] doubleValue], [frameValue[@"height"] doubleValue]);
    BOOL targetChanged = !doraPanel.hasTargetFrame || !NSEqualRects(doraPanel.targetFrame, target);
    doraPanel.targetFrame = target;
    doraPanel.hasTargetFrame = YES;
    [doraPanel.islandContent applyView:view];
    BOOL expanded = [view[@"expanded"] boolValue];
    doraPanel.hasShadow = expanded;
    doraPanel.islandContent.layer.cornerRadius = MIN(20, target.size.height / 2);
    if (!targetChanged) return;

    if (doraFrameAnimation != nil) {
        [doraFrameAnimation stopAnimation];
        doraFrameAnimation = nil;
    }
    BOOL animate = doraPanel.hasPresented && [view[@"animateFrame"] boolValue];
    if (!animate) {
        [doraPanel setFrame:target display:YES];
        doraPanel.hasPresented = YES;
        return;
    }
    NSDictionary *animation = @{
        NSViewAnimationTargetKey: doraPanel,
        NSViewAnimationStartFrameKey: [NSValue valueWithRect:doraPanel.frame],
        NSViewAnimationEndFrameKey: [NSValue valueWithRect:target],
    };
    doraFrameAnimation = [[NSViewAnimation alloc] initWithViewAnimations:@[animation]];
    doraFrameAnimation.duration = 0.20;
    doraFrameAnimation.animationCurve = NSAnimationEaseInOut;
    doraFrameAnimation.animationBlockingMode = NSAnimationNonblocking;
    doraFrameAnimation.frameRate = 60;
    [doraFrameAnimation startAnimation];
}

void doraIslandStart(void) {
    [NSApplication sharedApplication];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    NSScreen *screen = NSScreen.screens.firstObject ?: NSScreen.mainScreen;
    NSRect screenFrame = screen != nil ? screen.frame : NSMakeRect(0, 0, 1512, 982);
    CGFloat compactHeight = doraCompactHeight(screen);
    NSRect initial = NSMakeRect(NSMidX(screenFrame) - 180, NSMaxY(screenFrame) - compactHeight, 360, compactHeight);
    doraPanel = [[DoraIslandPanel alloc]
        initWithContentRect:initial styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
        backing:NSBackingStoreBuffered defer:NO];
    doraPanel.opaque = NO;
    doraPanel.backgroundColor = NSColor.clearColor;
    doraPanel.hasShadow = NO;
    doraPanel.hidesOnDeactivate = NO;
    doraPanel.level = NSStatusWindowLevel;
    doraPanel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
                                   NSWindowCollectionBehaviorFullScreenAuxiliary |
                                   NSWindowCollectionBehaviorStationary;
    doraPanel.becomesKeyOnlyIfNeeded = YES;
    doraPanel.islandContent = [[DoraIslandContentView alloc] initWithFrame:NSMakeRect(0, 0, 360, compactHeight)];
    doraPanel.contentView = doraPanel.islandContent;
    doraScreenObserver = [NSNotificationCenter.defaultCenter
        addObserverForName:NSApplicationDidChangeScreenParametersNotification object:nil queue:NSOperationQueue.mainQueue
        usingBlock:^(NSNotification *note) { doraSendScreen(); }];
    [doraPanel orderFrontRegardless];
    doraPointerKnown = NO;
    doraPointerTimer = [NSTimer timerWithTimeInterval:doraPointerSampleInterval repeats:YES block:^(NSTimer *timer) {
        doraSamplePointer();
    }];
    [NSRunLoop.mainRunLoop addTimer:doraPointerTimer forMode:NSRunLoopCommonModes];
    doraSamplePointer();
    doraSendScreen();
    [NSApp run];
}

int doraIslandPointerInside(void) {
    __block BOOL inside = NO;
    void (^check)(void) = ^{
        inside = doraCurrentPointerInside();
    };
    if (NSThread.isMainThread) check(); else dispatch_sync(dispatch_get_main_queue(), check);
    return inside ? 1 : 0;
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
        if (doraFrameAnimation != nil) {
            [doraFrameAnimation stopAnimation];
            doraFrameAnimation = nil;
        }
        [doraPointerTimer invalidate];
        doraPointerTimer = nil;
        doraPointerKnown = NO;
        [doraPanel close];
        doraPanel = nil;
        [NSApp stop:nil];
        NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint
            modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0];
        [NSApp postEvent:wake atStart:NO];
    });
}
