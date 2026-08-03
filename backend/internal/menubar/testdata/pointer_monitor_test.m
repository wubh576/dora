#import <Cocoa/Cocoa.h>
#import "pointer_monitor_darwin.h"

@interface DoraTestPointerMonitor : DoraPointerMonitorManager
@property(nonatomic) NSUInteger localAdds;
@property(nonatomic) NSUInteger globalAdds;
@property(nonatomic) NSUInteger removes;
@property(nonatomic) NSEventMask localMask;
@property(nonatomic) NSEventMask globalMask;
@property(nonatomic, copy) NSEvent * (^localHandler)(NSEvent *event);
@property(nonatomic, copy) void (^globalHandler)(NSEvent *event);
@end

@implementation DoraTestPointerMonitor
- (id)addLocalMonitorForEventsMatchingMask:(NSEventMask)mask
                                   handler:(NSEvent * (^)(NSEvent *event))handler {
    self.localAdds++;
    self.localMask = mask;
    self.localHandler = handler;
    return [NSObject new];
}
- (id)addGlobalMonitorForEventsMatchingMask:(NSEventMask)mask
                                    handler:(void (^)(NSEvent *event))handler {
    self.globalAdds++;
    self.globalMask = mask;
    self.globalHandler = handler;
    return [NSObject new];
}
- (void)removeMonitor:(id)monitor {
    if (monitor != nil) self.removes++;
}
@end

static int doraAssert(BOOL condition, const char *message) {
    if (condition) return 0;
    fprintf(stderr, "%s\n", message);
    return 1;
}

int main(void) {
    @autoreleasepool {
        __block NSUInteger samples = 0;
        DoraTestPointerMonitor *monitor = [[DoraTestPointerMonitor alloc] initWithSampler:^{ samples++; }];
        [monitor start];
        id firstLocal = monitor.localMonitor;
        id firstGlobal = monitor.globalMonitor;
        [monitor start];
        if (doraAssert(monitor.localAdds == 1 && monitor.globalAdds == 1,
                       "pointer monitors were installed more than once")) return 1;
        if (doraAssert(monitor.localMonitor == firstLocal && monitor.globalMonitor == firstGlobal,
                       "repeated start replaced pointer monitor tokens")) return 1;
        NSEventMask requiredMouseEvents = NSEventMaskMouseMoved |
                                               NSEventMaskLeftMouseDragged |
                                               NSEventMaskRightMouseDragged |
                                               NSEventMaskOtherMouseDragged;
        NSEventMask keyboardEvents = NSEventMaskKeyDown | NSEventMaskKeyUp | NSEventMaskFlagsChanged;
        if (doraAssert(monitor.localMask == requiredMouseEvents && monitor.globalMask == requiredMouseEvents,
                       "pointer monitor event mask is incomplete")) return 1;
        if (doraAssert((monitor.localMask & keyboardEvents) == 0 && (monitor.globalMask & keyboardEvents) == 0,
                       "pointer monitor unexpectedly listens for keyboard events")) return 1;
        if (doraAssert(DoraPointerEventMask() == requiredMouseEvents,
                       "pointer event mask helper differs from the required mouse events")) return 1;
        NSMutableString *interactionOrder = [NSMutableString string];
        DoraInteractionEventBlock begin = ^{ [interactionOrder appendString:@"start "]; };
        DoraInteractionEventBlock dispatch = ^{ [interactionOrder appendString:@"dispatch "]; };
        DoraInteractionEventBlock end = ^{ [interactionOrder appendString:@"end "]; };
        DoraDispatchInteractionEvent(NSEventTypeLeftMouseDown, begin, dispatch, end);
        if (doraAssert([interactionOrder isEqualToString:@"start dispatch end "],
                       "mouse down does not bracket the full AppKit control tracking loop")) return 1;
        [interactionOrder setString:@""];
        DoraDispatchInteractionEvent(NSEventTypeLeftMouseUp, begin, dispatch, end);
        if (doraAssert([interactionOrder isEqualToString:@"dispatch end "],
                       "standalone mouse up is not retained as an interaction-end fallback")) return 1;
        [interactionOrder setString:@""];
        DoraDispatchInteractionEvent(NSEventTypeMouseMoved, begin, dispatch, end);
        if (doraAssert([interactionOrder isEqualToString:@"dispatch "],
                       "mouse movement unexpectedly changes interaction state")) return 1;

        NSEvent *event = (NSEvent *)[NSObject new];
        if (doraAssert(monitor.localHandler(event) == event, "local pointer monitor swallowed or replaced its event")) return 1;
        monitor.globalHandler(event);
        [monitor panelDidFirstAppear];
        [monitor panelDidApplyFrame];
        [monitor screenParametersDidChange];
        NSAnimation *animation = [[NSAnimation alloc] initWithDuration:0 animationCurve:NSAnimationLinear];
        [monitor animationDidEnd:animation];
        if (doraAssert(samples == 6, "pointer sampling paths did not invoke the shared sampler")) return 1;

        BOOL known = NO;
        BOOL inside = NO;
        if (doraAssert(DoraUpdatePointerState(&known, &inside, NO), "initial pointer state was not published")) return 1;
        if (doraAssert(!DoraUpdatePointerState(&known, &inside, NO), "unchanged outside state was published twice")) return 1;
        if (doraAssert(DoraUpdatePointerState(&known, &inside, YES), "outside to inside transition was not published")) return 1;
        if (doraAssert(!DoraUpdatePointerState(&known, &inside, YES), "unchanged inside state was published twice")) return 1;

        [monitor stop];
        [monitor stop];
        if (doraAssert(monitor.removes == 2 && monitor.localMonitor == nil && monitor.globalMonitor == nil,
                       "pointer monitors were not removed exactly once")) return 1;
    }
    return 0;
}
