#import <Cocoa/Cocoa.h>
#import "approval_window_darwin.h"

static int decisionKind;
static long long decisionID;
void doraIslandOnEvent(int kind, long long value) {
    if (kind >= 10 && kind <= 12) { decisionKind = kind; decisionID = value; }
}
static void check(BOOL value, const char *message) {
    if (!value) { fprintf(stderr, "%s\n", message); exit(1); }
}
int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        NSDictionary *first = @{ @"id": @101, @"sessionId": @1, @"title": @"审批验证", @"tool": @"Bash", @"detail": @"{\n  \"command\": \"printf example\"\n}" };
        NSDictionary *second = @{ @"id": @102, @"sessionId": @1, @"title": @"另一次审批", @"tool": @"Bash", @"detail": @"{\"command\":\"pwd\"}" };
        doraUpdateApprovals(@[first, second]);
        doraShowApprovals(1);
        NSWindow *window = nil;
        for (NSWindow *candidate in NSApp.windows) if ([candidate.title isEqualToString:@"Codex · 本次工具授权"]) window = candidate;
        check(window != nil, "审批窗口没有打开");
        id controller = window.windowController;
        NSArray<NSButton *> *buttons = [controller valueForKey:@"actions"];
        NSTextView *detail = [controller valueForKey:@"detail"];
        NSPopUpButton *selector = [controller valueForKey:@"selector"];
        check(buttons.count == 3 && buttons[0].enabled, "审批操作不可用");
        check([detail.string containsString:@"printf example"], "审批详情缺失");
        check(buttons[0].keyEquivalent.length == 0, "允许不能绑定回车");
        doraUpdateApprovals(@[second]);
        check(!buttons[0].enabled && detail.string.length == 0, "请求结束后仍可操作或详情未清除");
        check(selector.selectedItem == nil, "自动选中了其他请求");
        [selector selectItemAtIndex:0];
        [NSApp sendAction:selector.action to:selector.target from:selector];
        check(buttons[0].enabled && [detail.string containsString:@"pwd"], "明确选择后未更新详情");
        [buttons[1] performClick:nil];
        check(decisionKind == 11 && decisionID == 102, "拒绝回传到了错误请求");
        check(!window.visible && detail.string.length == 0, "提交后窗口详情未清除");
        doraCloseApprovals();
    }
    return 0;
}
