#import "pointer_monitor_darwin.h"

NSEventMask DoraPointerEventMask(void) {
    return NSEventMaskMouseMoved |
           NSEventMaskLeftMouseDragged |
           NSEventMaskRightMouseDragged |
           NSEventMaskOtherMouseDragged;
}

BOOL DoraUpdatePointerState(BOOL *known, BOOL *inside, BOOL nextInside) {
    if (*known && *inside == nextInside) return NO;
    *known = YES;
    *inside = nextInside;
    return YES;
}

@interface DoraPointerMonitorManager ()
@property(nonatomic, copy) DoraPointerSampleBlock sampler;
@property(nonatomic, strong, readwrite) id localMonitor;
@property(nonatomic, strong, readwrite) id globalMonitor;
@property(nonatomic) BOOL started;
@end

@implementation DoraPointerMonitorManager
- (instancetype)initWithSampler:(DoraPointerSampleBlock)sampler {
    self = [super init];
    if (self != nil) self.sampler = sampler;
    return self;
}

- (void)start {
    if (self.started) return;
    self.started = YES;
    __weak DoraPointerMonitorManager *weakSelf = self;
    self.localMonitor = [self addLocalMonitorForEventsMatchingMask:DoraPointerEventMask()
        handler:^NSEvent *(NSEvent *event) {
            DoraPointerMonitorManager *manager = weakSelf;
            return manager == nil ? event : [manager handleLocalEvent:event];
        }];
    self.globalMonitor = [self addGlobalMonitorForEventsMatchingMask:DoraPointerEventMask()
        handler:^(NSEvent *event) {
            [weakSelf handleGlobalEvent:event];
        }];
}

- (void)stop {
    if (!self.started) return;
    self.started = NO;
    if (self.localMonitor != nil) [self removeMonitor:self.localMonitor];
    if (self.globalMonitor != nil) [self removeMonitor:self.globalMonitor];
    self.localMonitor = nil;
    self.globalMonitor = nil;
}

- (NSEvent *)handleLocalEvent:(NSEvent *)event {
    if (self.sampler != nil) self.sampler();
    return event;
}

- (void)handleGlobalEvent:(NSEvent *)event {
    (void)event;
    if (self.sampler != nil) self.sampler();
}

- (void)samplePointer {
    if (self.sampler != nil) self.sampler();
}

- (void)panelDidFirstAppear {
    [self samplePointer];
}

- (void)panelDidApplyFrame {
    [self samplePointer];
}

- (void)screenParametersDidChange {
    [self samplePointer];
}

- (void)animationDidEnd:(NSAnimation *)animation {
    (void)animation;
    [self panelDidApplyFrame];
}

- (id)addLocalMonitorForEventsMatchingMask:(NSEventMask)mask
                                   handler:(NSEvent * (^)(NSEvent *event))handler {
    return [NSEvent addLocalMonitorForEventsMatchingMask:mask handler:handler];
}

- (id)addGlobalMonitorForEventsMatchingMask:(NSEventMask)mask
                                    handler:(void (^)(NSEvent *event))handler {
    return [NSEvent addGlobalMonitorForEventsMatchingMask:mask handler:handler];
}

- (void)removeMonitor:(id)monitor {
    [NSEvent removeMonitor:monitor];
}
@end
