//go:build darwin && cgo

// The menu bar icon the Mac app shows while it runs: Open, Show Log, Quit.
// Called from app_darwin.go on the main thread; it does not return.

#import <Cocoa/Cocoa.h>

@interface LCMenuBar : NSObject <NSApplicationDelegate>
@property(strong) NSStatusItem *item;
@property(copy) NSString *title;
@property(copy) NSString *url;
@property(copy) NSString *logPath;
@end

@implementation LCMenuBar

- (void)applicationDidFinishLaunching:(NSNotification *)note {
  self.item = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
  NSImage *img = [NSImage imageWithSystemSymbolName:@"point.3.connected.trianglepath.dotted"
                           accessibilityDescription:self.title];
  if (!img) img = [NSImage imageWithSystemSymbolName:@"network" accessibilityDescription:self.title];
  if (img) {
    img.template = YES;
    self.item.button.image = img;
  } else {
    self.item.button.title = @"LC";
  }
  self.item.button.toolTip = self.title;

  NSMenu *menu = [[NSMenu alloc] init];
  [menu addItemWithTitle:self.title action:nil keyEquivalent:@""]; // no action: shown greyed, as a heading
  [menu addItemWithTitle:self.url action:nil keyEquivalent:@""];
  [menu addItem:[NSMenuItem separatorItem]];
  [menu addItemWithTitle:@"Open in Browser" action:@selector(openPage:) keyEquivalent:@""].target = self;
  [menu addItemWithTitle:@"Show Log" action:@selector(showLog:) keyEquivalent:@""].target = self;
  [menu addItem:[NSMenuItem separatorItem]];
  [menu addItemWithTitle:@"Quit Luminex Commission" action:@selector(terminate:) keyEquivalent:@"q"].target = NSApp;
  self.item.menu = menu;
}

- (void)openPage:(id)sender {
  [[NSWorkspace sharedWorkspace] openURL:[NSURL URLWithString:self.url]];
}

- (void)showLog:(id)sender {
  [[NSWorkspace sharedWorkspace] openURL:[NSURL fileURLWithPath:self.logPath]];
}

// Clicking the app again (Dock, Finder, Launchpad) while it runs opens the page.
- (BOOL)applicationShouldHandleReopen:(NSApplication *)app hasVisibleWindows:(BOOL)visible {
  [self openPage:nil];
  return NO;
}

@end

void lcRunMenuBar(const char *title, const char *url, const char *logPath) {
  @autoreleasepool {
    static LCMenuBar *menuBar; // the application holds its delegate weakly
    menuBar = [[LCMenuBar alloc] init];
    menuBar.title = [NSString stringWithUTF8String:title];
    menuBar.url = [NSString stringWithUTF8String:url];
    menuBar.logPath = [NSString stringWithUTF8String:logPath];
    NSApplication *app = [NSApplication sharedApplication];
    app.delegate = menuBar;
    [app setActivationPolicy:NSApplicationActivationPolicyAccessory];
    [app run];
  }
}
