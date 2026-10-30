import Cocoa
import FlutterMacOS

class MainFlutterWindow: NSWindow {
  override func awakeFromNib() {
    let flutterViewController = FlutterViewController()
    self.contentViewController = flutterViewController

    // The app draws under the title bar (lib/widgets/desktop_frame.dart);
    // only the traffic-light buttons stay. Set here, not only from Dart, so
    // the first frame already has no title bar strip.
    styleMask.insert(.fullSizeContentView)
    titlebarAppearsTransparent = true
    titleVisibility = .hidden
    isMovableByWindowBackground = false
    title = "环游"

    // Phone-first UI: one fixed size (see lib/widgets/desktop_frame.dart).
    // No resizing, no zoom, no full screen.
    styleMask.remove(.resizable)
    collectionBehavior.insert(.fullScreenNone)
    let size = NSSize(width: 420, height: 820)
    setContentSize(size)
    contentMinSize = size
    contentMaxSize = size
    standardWindowButton(.zoomButton)?.isEnabled = false
    center()

    RegisterGeneratedPlugins(registry: flutterViewController)
    super.awakeFromNib()
  }
}
