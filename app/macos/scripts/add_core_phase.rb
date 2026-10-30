#!/usr/bin/env ruby
# Adds the "Embed VPN core" build phase to the macOS Runner target: copies
# core/huanyou-core (built by mobile/build.sh macos) into
# 环游.app/Contents/MacOS and signs it with the app's identity, before Xcode
# signs the app itself. Idempotent. Requires the `xcodeproj` gem.
#
#   cd app/macos && ruby scripts/add_core_phase.rb
require 'xcodeproj'

MACOS_DIR = File.expand_path('..', __dir__)
NAME = 'Embed VPN core'
project = Xcodeproj::Project.open(File.join(MACOS_DIR, 'Runner.xcodeproj'))
runner = project.targets.find { |t| t.name == 'Runner' } or abort 'Runner target not found'

phase = runner.shell_script_build_phases.find { |p| p.name == NAME } || runner.new_shell_script_build_phase(NAME)
phase.shell_path = '/bin/sh'
phase.input_paths = ['$(SRCROOT)/core/huanyou-core']
phase.output_paths = ['$(TARGET_BUILD_DIR)/$(EXECUTABLE_FOLDER_PATH)/huanyou-core']
phase.shell_script = <<~'SH'
  set -e
  SRC="$SRCROOT/core/huanyou-core"
  if [ ! -f "$SRC" ]; then
    echo "error: missing $SRC. Run: mobile/build.sh macos"
    exit 1
  fi
  DST="$TARGET_BUILD_DIR/$EXECUTABLE_FOLDER_PATH/huanyou-core"
  cp -f "$SRC" "$DST"
  if [ "${CODE_SIGNING_ALLOWED:-YES}" = "YES" ]; then
    /usr/bin/codesign --force --options runtime --timestamp=none \
      --sign "${EXPANDED_CODE_SIGN_IDENTITY:--}" "$DST"
  fi
SH
project.save
puts "#{NAME}: ok"
