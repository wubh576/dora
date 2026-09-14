#import "approval_window_darwin.h"

extern void doraIslandOnEvent(int kind, long long value);

@interface DoraApprovalWindow : NSWindowController <NSWindowDelegate>
@property(nonatomic) long long sessionID;
@property(nonatomic) long long selectedID;
@property(nonatomic, strong) NSArray<NSDictionary *> *requests;
@property(nonatomic, strong) NSPopUpButton *selector;
@property(nonatomic, strong) NSTextView *detail;
@property(nonatomic, strong) NSTextField *status;
@property(nonatomic, strong) NSArray<NSButton *> *actions;
@property(nonatomic, strong) NSArray<NSNumber *> *menuIDs;
- (void)refresh;
@end

@implementation DoraApprovalWindow
- (instancetype)init {
    NSWindow *window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 720, 500)
        styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskResizable
        backing:NSBackingStoreBuffered defer:NO];
    self = [super initWithWindow:window];
    if (self) {
        window.title = @"Codex · 本次工具授权";
        window.minSize = NSMakeSize(580, 380);
        window.releasedWhenClosed = NO;
        window.level = NSFloatingWindowLevel;
        window.delegate = self;
        NSView *content = window.contentView;
        self.selector = [[NSPopUpButton alloc] initWithFrame:NSMakeRect(20, 454, 680, 28) pullsDown:NO];
        self.selector.autoresizingMask = NSViewWidthSizable | NSViewMinYMargin;
        self.selector.target = self;
        self.selector.action = @selector(selectRequest:);
        [content addSubview:self.selector];
        NSScrollView *scroll = [[NSScrollView alloc] initWithFrame:NSMakeRect(20, 94, 680, 348)];
        scroll.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
        scroll.hasVerticalScroller = YES;
        scroll.borderType = NSBezelBorder;
        self.detail = [[NSTextView alloc] initWithFrame:scroll.bounds];
        self.detail.editable = NO;
        self.detail.font = [NSFont monospacedSystemFontOfSize:12 weight:NSFontWeightRegular];
        self.detail.textContainerInset = NSMakeSize(10, 10);
        self.detail.autoresizingMask = NSViewWidthSizable;
        self.detail.textContainer.widthTracksTextView = YES;
        scroll.documentView = self.detail;
        [content addSubview:scroll];
        self.status = [NSTextField labelWithString:@""];
        self.status.frame = NSMakeRect(20, 60, 680, 24);
        self.status.autoresizingMask = NSViewWidthSizable;
        self.status.font = [NSFont systemFontOfSize:12];
        [content addSubview:self.status];
        NSMutableArray *buttons = [NSMutableArray array];
        NSArray *titles = @[@"本次允许", @"本次拒绝", @"转到原应用"];
        for (NSInteger i = 0; i < 3; i++) {
            NSButton *button = [NSButton buttonWithTitle:titles[i] target:self action:@selector(decide:)];
            button.frame = NSMakeRect(340 + i * 120, 16, 110, 32);
            button.autoresizingMask = NSViewMinXMargin;
            button.tag = 10 + i;
            // 不绑定回车快捷键，避免窗口刚出现时误批准。
            [content addSubview:button];
            [buttons addObject:button];
        }
        self.actions = buttons;
        [window center];
    }
    return self;
}
- (void)refresh {
    NSMutableArray<NSNumber *> *ids = [NSMutableArray array];
    for (NSDictionary *request in self.requests) {
        if ([request[@"sessionId"] longLongValue] == self.sessionID) [ids addObject:request[@"id"]];
    }
    BOOL rebuild = ![ids isEqualToArray:self.menuIDs];
    if (rebuild) { [self.selector removeAllItems]; self.menuIDs = ids; }
    NSDictionary *selected = nil;
    NSInteger index = 0;
    for (NSDictionary *request in self.requests) {
        if ([request[@"sessionId"] longLongValue] != self.sessionID) continue;
        if (rebuild) {
            NSString *label = [NSString stringWithFormat:@"%ld · %@ · %@", (long)index + 1, request[@"title"] ?: @"Codex", request[@"tool"] ?: @"工具"];
            NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:label action:NULL keyEquivalent:@""];
            item.representedObject = request[@"id"];
            [self.selector.menu addItem:item];
        }
        if ([request[@"id"] longLongValue] == self.selectedID) {
            selected = request;
            if (rebuild || [self.selector.selectedItem.representedObject longLongValue] != self.selectedID) [self.selector selectItemAtIndex:index];
        }
        index++;
    }
    BOOL valid = selected != nil;
    if (!valid) [self.selector selectItem:nil];
    NSString *text = valid ? selected[@"detail"] : @"";
    if (![self.detail.string isEqualToString:text]) self.detail.string = text ?: @"";
    self.status.stringValue = valid ? @"仅处理这一次请求；超时后转回 Codex 原生审批。" : @"此请求已结束或已转回 Codex。其他请求需从上方明确选择。";
    for (NSButton *button in self.actions) button.enabled = valid;
}
- (void)selectRequest:(id)sender {
    (void)sender;
    self.selectedID = [self.selector.selectedItem.representedObject longLongValue];
    [self refresh];
}
- (void)decide:(NSButton *)sender {
    long long requestID = self.selectedID;
    for (NSButton *button in self.actions) button.enabled = NO;
    self.selectedID = 0;
    self.detail.string = @"";
    [self.window orderOut:nil];
    doraIslandOnEvent((int)sender.tag, requestID);
    doraIslandOnEvent(8, 0);
}
- (void)windowWillClose:(NSNotification *)notification {
    (void)notification;
    self.detail.string = @"";
    self.selectedID = 0;
    doraIslandOnEvent(8, 0);
}
@end

static DoraApprovalWindow *approvalWindow;
static NSArray<NSDictionary *> *latestApprovals;

void doraUpdateApprovals(NSArray<NSDictionary *> *requests) {
    latestApprovals = [requests copy];
    approvalWindow.requests = latestApprovals;
    [approvalWindow refresh];
}

void doraShowApprovals(long long sessionID) {
    if (!approvalWindow) approvalWindow = [[DoraApprovalWindow alloc] init];
    approvalWindow.sessionID = sessionID;
    approvalWindow.requests = latestApprovals;
    approvalWindow.selectedID = 0;
    for (NSDictionary *request in latestApprovals) {
        if ([request[@"sessionId"] longLongValue] == sessionID) { approvalWindow.selectedID = [request[@"id"] longLongValue]; break; }
    }
    [approvalWindow refresh];
    doraIslandOnEvent(7, 0);
    [NSApp activateIgnoringOtherApps:YES];
    [approvalWindow showWindow:nil];
    [approvalWindow.window makeKeyAndOrderFront:nil];
}

void doraCloseApprovals(void) {
    [approvalWindow close];
    approvalWindow = nil;
    latestApprovals = nil;
}
