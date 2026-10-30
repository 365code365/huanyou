import Flutter
import NetworkExtension

/// Flutter bridge that drives the PacketTunnel extension through NETunnelProviderManager.
final class VpnPlugin: NSObject, FlutterStreamHandler {
  private static var shared: VpnPlugin?
  private static let tunnelBundleId = (Bundle.main.bundleIdentifier ?? "") + ".PacketTunnel"

  private var manager: NETunnelProviderManager?
  private var sink: FlutterEventSink?
  private var observer: NSObjectProtocol?
  private var wasConnecting = false

  static func register(with registrar: FlutterPluginRegistrar) {
    let plugin = VpnPlugin()
    shared = plugin
    FlutterMethodChannel(name: "vpn_platform/vpn", binaryMessenger: registrar.messenger())
      .setMethodCallHandler(plugin.handle)
    FlutterEventChannel(name: "vpn_platform/vpn_status", binaryMessenger: registrar.messenger())
      .setStreamHandler(plugin)
    #if !targetEnvironment(simulator)
      // Clean up the "需要更新" entry left in Settings by a build without the extension.
      if !tunnelExtensionInstalled { removeStaleProfiles() }
    #endif
    plugin.loadManager { _ in }
  }

  private func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
    switch call.method {
    case "status":
      loadManager { m in result(Self.statusName(m?.connection.status ?? .disconnected)) }
    case "connect":
      guard let args = call.arguments as? [String: Any], let config = args["config"] as? String else {
        result(FlutterError(code: "bad_args", message: "缺少配置", details: nil))
        return
      }
      connect(config: config, name: args["name"] as? String ?? "VPN", result: result)
    case "disconnect":
      manager?.connection.stopVPNTunnel()
      result(nil)
    default:
      result(FlutterMethodNotImplemented)
    }
  }

  private func loadManager(_ done: @escaping (NETunnelProviderManager?) -> Void) {
    NETunnelProviderManager.loadAllFromPreferences { [weak self] managers, _ in
      DispatchQueue.main.async {
        let m = managers?.first {
          ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.tunnelBundleId
        }
        if let m { self?.attach(m) }
        done(m)
      }
    }
  }

  /// The PacketTunnel extension must be inside this app bundle. If it isn't
  /// (e.g. a build signed without an extension profile), iOS would save the
  /// VPN profile anyway and then show it as "需要更新" in Settings.
  private static var tunnelExtensionInstalled: Bool {
    guard let url = Bundle.main.builtInPlugInsURL?.appendingPathComponent("PacketTunnel.appex") else { return false }
    return Bundle(url: url)?.bundleIdentifier == tunnelBundleId
  }

  /// Removes VPN profiles this app saved earlier that can no longer work.
  private static func removeStaleProfiles() {
    NETunnelProviderManager.loadAllFromPreferences { managers, _ in
      for m in managers ?? [] { m.removeFromPreferences { _ in } }
    }
  }

  private func connect(config: String, name: String, result: @escaping FlutterResult) {
    #if targetEnvironment(simulator)
      // Apple does not run Network Extensions in the Simulator.
      result(FlutterError(code: "unsupported", message: "iOS 模拟器不支持 VPN，请在真机上测试连接", details: nil))
      return
    #endif
    guard Self.tunnelExtensionInstalled else {
      Self.removeStaleProfiles()
      result(FlutterError(
        code: "no_extension",
        message: "当前安装包缺少 VPN 组件（PacketTunnel 扩展未签名），无法建立连接。请安装包含 VPN 扩展的正式版本。",
        details: nil))
      return
    }
    loadManager { [weak self] existing in
      guard let self else { return }
      let m = existing ?? NETunnelProviderManager()
      let proto = NETunnelProviderProtocol()
      proto.providerBundleIdentifier = Self.tunnelBundleId
      proto.serverAddress = name
      // Kept in the VPN preferences so the system can restart the tunnel on its own.
      proto.providerConfiguration = ["config": config]
      m.protocolConfiguration = proto
      m.localizedDescription = "环游"
      m.isEnabled = true
      // The first save shows the system "Add VPN Configurations" prompt.
      m.saveToPreferences { error in
        if let error {
          DispatchQueue.main.async {
            result(FlutterError(code: "save_failed", message: "需要允许添加 VPN 配置：\(error.localizedDescription)", details: nil))
          }
          return
        }
        m.loadFromPreferences { _ in
          DispatchQueue.main.async {
            self.attach(m)
            do {
              self.wasConnecting = true
              try m.connection.startVPNTunnel()
              result(nil)
            } catch {
              result(FlutterError(code: "start_failed", message: error.localizedDescription, details: nil))
            }
          }
        }
      }
    }
  }

  private func attach(_ m: NETunnelProviderManager) {
    manager = m
    if let observer { NotificationCenter.default.removeObserver(observer) }
    observer = NotificationCenter.default.addObserver(
      forName: .NEVPNStatusDidChange, object: m.connection, queue: .main
    ) { [weak self] _ in self?.emit() }
    emit()
  }

  private func emit() {
    guard let status = manager?.connection.status else { return }
    if status == .connected { wasConnecting = false }
    // If the tunnel stops while we were trying to connect, report the extension's error.
    if status == .disconnected, wasConnecting, #available(iOS 16.0, *) {
      wasConnecting = false
      manager?.connection.fetchLastDisconnectError { [weak self] error in
        DispatchQueue.main.async {
          if let error {
            self?.sink?(["status": "error", "message": error.localizedDescription])
          } else {
            self?.sink?(["status": "disconnected"])
          }
        }
      }
      return
    }
    sink?(["status": Self.statusName(status)])
  }

  private static func statusName(_ s: NEVPNStatus) -> String {
    switch s {
    case .connecting, .reasserting: return "connecting"
    case .connected: return "connected"
    case .disconnecting: return "disconnecting"
    default: return "disconnected"
    }
  }

  func onListen(withArguments _: Any?, eventSink events: @escaping FlutterEventSink) -> FlutterError? {
    sink = events
    emit()
    return nil
  }

  func onCancel(withArguments _: Any?) -> FlutterError? {
    sink = nil
    return nil
  }
}
