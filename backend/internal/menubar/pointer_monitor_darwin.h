#import <Cocoa/Cocoa.h>

typedef void (^DoraPointerSampleBlock)(void);
typedef void (^DoraInteractionEventBlock)(void);

NSEventMask DoraPointerEventMask(void);
BOOL DoraUpdatePointerState(BOOL *known, BOOL *inside, BOOL nextInside);
void DoraDispatchInteractionEvent(NSEventType type,
                                  DoraInteractionEventBlock begin,
                                  DoraInteractionEventBlock dispatch,
                                  DoraInteractionEventBlock end);

@interface DoraPointerMonitorManager : NSObject <NSAnimationDelegate>
@property(nonatomic, strong, readonly) id localMonitor;
@property(nonatomic, strong, readonly) id globalMonitor;
- (instancetype)initWithSampler:(DoraPointerSampleBlock)sampler;
- (void)start;
- (void)stop;
- (void)panelDidFirstAppear;
- (void)panelDidApplyFrame;
- (void)screenParametersDidChange;
- (NSEvent *)handleLocalEvent:(NSEvent *)event;
- (void)handleGlobalEvent:(NSEvent *)event;

// 这三个方法只形成可替换的 AppKit 边界，生产实现直接调用 NSEvent。
- (id)addLocalMonitorForEventsMatchingMask:(NSEventMask)mask
                                   handler:(NSEvent * (^)(NSEvent *event))handler;
- (id)addGlobalMonitorForEventsMatchingMask:(NSEventMask)mask
                                    handler:(void (^)(NSEvent *event))handler;
- (void)removeMonitor:(id)monitor;
@end
