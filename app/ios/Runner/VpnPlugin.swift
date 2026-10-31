import Flutter
import MachO
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
    // Integrity report for the API (lib/core/integrity.dart). Other device
    // methods are not implemented on iOS (the app falls back on its own).
    FlutterMethodChannel(name: "vpn_platform/device", binaryMessenger: registrar.messenger())
      .setMethodCallHandler { call, result in
        if call.method == "integrity" {
          DispatchQueue.global(qos: .utility).async {
            let r = Integrity.check()
            DispatchQueue.main.async { result(r) }
          }
        } else {
          result(FlutterMethodNotImplemented)
        }
      }
    #if !targetEnvironment(simulator)
      // Clean up the "需要更新" entry left in Settings by a build without the extension.
      if !tunnelExtensionInstalled { removeStaleProfiles() }
    #endif
    plugin.loadManager { _ in }
  }

  private func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
    switch call.method {
    case "status":
      loadManager(emit: false) { m in result(Self.statusName(m?.connection.status ?? .disconnected)) }
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

  /// emit: false on the connect / status paths, where the manager's current
  /// status (still "disconnected" before the start) must not be reported:
  /// it would flip the app from 连接中 back to 未连接 mid-connect.
  private func loadManager(emit: Bool = true, _ done: @escaping (NETunnelProviderManager?) -> Void) {
    NETunnelProviderManager.loadAllFromPreferences { [weak self] managers, _ in
      DispatchQueue.main.async {
        let m = managers?.first {
          ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.tunnelBundleId
        }
        if let m { self?.attach(m, emit: emit) }
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
    loadManager(emit: false) { [weak self] existing in
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
            self.attach(m, emit: false)
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

  private func attach(_ m: NETunnelProviderManager, emit shouldEmit: Bool = true) {
    manager = m
    if let observer { NotificationCenter.default.removeObserver(observer) }
    observer = NotificationCenter.default.addObserver(
      forName: .NEVPNStatusDidChange, object: m.connection, queue: .main
    ) { [weak self] _ in self?.emit() }
    if shouldEmit { emit() }
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
    var event: [String: Any] = ["status": Self.statusName(status)]
    // When the tunnel came up (ms), so a re-launched app shows the real time.
    if status == .connected, let d = manager?.connection.connectedDate {
      event["since"] = Int64(d.timeIntervalSince1970 * 1000)
    }
    sink?(event)
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

/// dbg: a debugger is attached (P_TRACED); hook: Frida / Substrate / other
/// injection libraries are loaded, or frida-server listens locally; root:
/// jailbreak traces. Reported to the server, which decides.
enum Integrity {
  static func check() -> [String: Any] {
    ["dbg": debugged(), "hook": hooked(), "root": jailbroken()]
  }

  private static func debugged() -> Bool {
    var info = kinfo_proc()
    var size = MemoryLayout<kinfo_proc>.stride
    var mib: [Int32] = [CTL_KERN, KERN_PROC, KERN_PROC_PID, getpid()]
    guard sysctl(&mib, UInt32(mib.count), &info, &size, nil, 0) == 0 else { return false }
    return (info.kp_proc.p_flag & P_TRACED) != 0
  }

  private static let hookLibs = ["frida", "gadget", "substrate", "substitute", "libhooker", "tweakinject", "cycript", "sslkillswitch", "ellekit"]

  private static func hooked() -> Bool {
    for i in 0..<_dyld_image_count() {
      guard let c = _dyld_get_image_name(i) else { continue }
      let name = String(cString: c).lowercased()
      if hookLibs.contains(where: { name.contains($0) }) { return true }
    }
    return fridaPort()
  }

  private static func fridaPort() -> Bool {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    guard fd >= 0 else { return false }
    defer { close(fd) }
    var addr = sockaddr_in()
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = in_port_t(UInt16(27042).bigEndian)
    addr.sin_addr.s_addr = inet_addr("127.0.0.1")
    // Non-blocking connect with a short wait.
    let flags = fcntl(fd, F_GETFL, 0)
    _ = fcntl(fd, F_SETFL, flags | O_NONBLOCK)
    let rc = withUnsafePointer(to: &addr) {
      $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
    }
    if rc == 0 { return true }
    guard errno == EINPROGRESS else { return false }
    var pfd = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
    guard poll(&pfd, 1, 150) == 1 else { return false }
    var err: Int32 = 0
    var len = socklen_t(MemoryLayout<Int32>.size)
    getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &len)
    return err == 0
  }

  private static func jailbroken() -> Bool {
    let paths = ["/Applications/Cydia.app", "/Applications/Sileo.app", "/var/jb", "/private/var/lib/apt",
                 "/usr/sbin/sshd", "/bin/bash", "/etc/apt", "/private/var/stash"]
    if paths.contains(where: { FileManager.default.fileExists(atPath: $0) }) { return true }
    // A sandboxed app cannot write outside its container.
    let probe = "/private/huanyou-jb-probe"
    if (try? "x".write(toFile: probe, atomically: false, encoding: .utf8)) != nil {
      try? FileManager.default.removeItem(atPath: probe)
      return true
    }
    return false
  }
}
