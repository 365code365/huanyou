import NetworkExtension
import Vpncore

/// Runs Xray-core (Vpncore.xcframework) on the utun interface iOS creates for us.
final class PacketTunnelProvider: NEPacketTunnelProvider {
  override func startTunnel(options: [String: NSObject]?, completionHandler: @escaping (Error?) -> Void) {
    guard
      let proto = protocolConfiguration as? NETunnelProviderProtocol,
      let config = proto.providerConfiguration?["config"] as? String
    else {
      completionHandler(Self.error("缺少配置，请在应用内重新连接"))
      return
    }

    let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "254.1.1.1")
    let v4 = NEIPv4Settings(addresses: ["172.19.0.1"], subnetMasks: ["255.255.255.252"])
    v4.includedRoutes = [NEIPv4Route.default()]
    settings.ipv4Settings = v4
    // Route IPv6 into the tunnel too so it cannot leak around the VPN.
    let v6 = NEIPv6Settings(addresses: ["fdfe:dcba:9876::1"], networkPrefixLengths: [126])
    v6.includedRoutes = [NEIPv6Route.default()]
    settings.ipv6Settings = v6
    // Xray answers DNS sent to 172.19.0.2 and resolves it through the proxy.
    let dns = NEDNSSettings(servers: ["172.19.0.2"])
    dns.matchDomains = [""]
    settings.dnsSettings = dns
    settings.mtu = 1500

    setTunnelNetworkSettings(settings) { error in
      if let error {
        completionHandler(error)
        return
      }
      let fd = VpncoreFindTunFd()
      guard fd > 0 else {
        completionHandler(Self.error("找不到 TUN 设备"))
        return
      }
      // Network Extensions are killed above ~50 MB.
      VpncoreSetMemoryLimit(30)
      var err: NSError?
      // geoip-only-cn-private.dat is bundled in this extension's resources.
      VpncoreStart(config, fd, Bundle.main.resourcePath, nil, &err)
      completionHandler(err)
    }
  }

  override func stopTunnel(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
    var err: NSError?
    VpncoreStop(&err)
    completionHandler()
  }

  private static func error(_ msg: String) -> NSError {
    NSError(domain: "PacketTunnel", code: 1, userInfo: [NSLocalizedDescriptionKey: msg])
  }
}
