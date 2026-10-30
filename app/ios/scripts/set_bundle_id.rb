#!/usr/bin/env ruby
# Sets the app's bundle ID (and the PacketTunnel extension's, as <id>.PacketTunnel)
# plus the development team in Runner.xcodeproj. Idempotent.
#
#   ruby scripts/set_bundle_id.rb app.example.vpn TEAMID
require 'xcodeproj'

bundle_id, team = ARGV
abort 'usage: set_bundle_id.rb <bundle-id> <team-id>' unless bundle_id && team

project = Xcodeproj::Project.open(File.expand_path('../Runner.xcodeproj', __dir__))
ids = { 'Runner' => bundle_id, 'PacketTunnel' => "#{bundle_id}.PacketTunnel", 'RunnerTests' => "#{bundle_id}.RunnerTests" }
project.targets.each do |t|
  next unless ids[t.name]
  t.build_configurations.each do |c|
    c.build_settings['PRODUCT_BUNDLE_IDENTIFIER'] = ids[t.name]
    c.build_settings['DEVELOPMENT_TEAM'] = team
  end
end
project.save
ids.each { |k, v| puts "#{k}: #{v}" }
