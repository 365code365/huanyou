#!/usr/bin/env ruby
# Adds the PacketTunnel Network Extension target to Runner.xcodeproj.
# Idempotent: safe to re-run. Requires the `xcodeproj` gem (ships with CocoaPods).
#
#   cd app/ios && ruby scripts/setup_packet_tunnel.rb
require 'xcodeproj'

IOS_DIR = File.expand_path('..', __dir__)
project = Xcodeproj::Project.open(File.join(IOS_DIR, 'Runner.xcodeproj'))
runner = project.targets.find { |t| t.name == 'Runner' } or abort 'Runner target not found'
MIN_IOS = '15.0'

if project.targets.any? { |t| t.name == 'PacketTunnel' }
  puts 'PacketTunnel target already exists, nothing to do'
  exit 0
end

runner_bundle_id = runner.build_configurations.first.build_settings['PRODUCT_BUNDLE_IDENTIFIER']

# --- files ---
group = project.main_group.new_group('PacketTunnel', 'PacketTunnel')
provider = group.new_reference('PacketTunnelProvider.swift')
group.new_reference('Info.plist')
group.new_reference('PacketTunnel.entitlements')
xcconfig = group.new_reference('PacketTunnel.xcconfig')
geo = group.new_reference('../../assets/geo/geoip-only-cn-private.dat')

fw_group = project.main_group['Frameworks'] || project.main_group.new_group('Frameworks')
vpncore = fw_group.new_reference(File.join(IOS_DIR, 'Frameworks/Vpncore.xcframework'))
vpncore.set_source_tree('SOURCE_ROOT')
vpncore.path = 'Frameworks/Vpncore.xcframework'

runner_group = project.main_group['Runner']
plugin = runner_group.new_reference('VpnPlugin.swift')
runner_group.new_reference('Runner.entitlements')
runner.add_file_references([plugin])

# --- extension target ---
ext = project.new_target(:app_extension, 'PacketTunnel', :ios, MIN_IOS, nil, :swift)
ext.add_file_references([provider])
ext.resources_build_phase.add_file_reference(geo)
ext.frameworks_build_phase.add_file_reference(vpncore) # gomobile output is a static framework: link, don't embed
ext.add_system_framework('NetworkExtension')

ext.build_configurations.each do |c|
  c.base_configuration_reference = xcconfig
  s = c.build_settings
  s['PRODUCT_BUNDLE_IDENTIFIER'] = "#{runner_bundle_id}.PacketTunnel"
  s['PRODUCT_NAME'] = '$(TARGET_NAME)'
  s['INFOPLIST_FILE'] = 'PacketTunnel/Info.plist'
  s['GENERATE_INFOPLIST_FILE'] = 'NO'
  s['CODE_SIGN_ENTITLEMENTS'] = 'PacketTunnel/PacketTunnel.entitlements'
  s['CODE_SIGN_STYLE'] = 'Automatic'
  s['SWIFT_VERSION'] = '5.0'
  s['IPHONEOS_DEPLOYMENT_TARGET'] = MIN_IOS
  s['TARGETED_DEVICE_FAMILY'] = '1,2'
  s['APPLICATION_EXTENSION_API_ONLY'] = 'YES'
  s['SKIP_INSTALL'] = 'YES'
  s['ENABLE_BITCODE'] = 'NO'
  s['FRAMEWORK_SEARCH_PATHS'] = ['$(inherited)', '$(PROJECT_DIR)/Frameworks']
  s['OTHER_LDFLAGS'] = ['$(inherited)', '-lresolv'] # Go's net package needs libresolv
  s['LD_RUNPATH_SEARCH_PATHS'] = ['$(inherited)', '@executable_path/Frameworks', '@executable_path/../../Frameworks']
end

# --- embed into Runner ---
runner.add_dependency(ext)
embed = runner.new_copy_files_build_phase('Embed Foundation Extensions')
embed.symbol_dst_subfolder_spec = :plug_ins
embed.add_file_reference(ext.product_reference, true).settings = { 'ATTRIBUTES' => ['RemoveHeadersOnCopy'] }
# Must run before Flutter's "Thin Binary" script, otherwise Xcode reports a build cycle.
phases = runner.build_phases
phases.delete(embed)
thin = phases.index { |p| p.respond_to?(:name) && p.name == 'Thin Binary' } || phases.length
phases.insert(thin, embed)

runner.build_configurations.each do |c|
  c.build_settings['CODE_SIGN_ENTITLEMENTS'] = 'Runner/Runner.entitlements'
  c.build_settings['IPHONEOS_DEPLOYMENT_TARGET'] = MIN_IOS
end
project.build_configurations.each { |c| c.build_settings['IPHONEOS_DEPLOYMENT_TARGET'] = MIN_IOS }

project.save
puts "Added PacketTunnel (#{runner_bundle_id}.PacketTunnel)"
