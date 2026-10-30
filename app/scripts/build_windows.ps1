# Builds the Windows release and zips it for upload in the admin panel
# (Releases -> publish Windows). Run on Windows from app/:
#   powershell -ExecutionPolicy Bypass -File scripts\build_windows.ps1 [-ApiBaseUrls https://api.example.com]
# Needs Flutter with Visual Studio 2022 ("Desktop development with C++").
# The VPN core must already be at windows\core\huanyou-core.exe:
#   bash ../mobile/build.sh windows   (works on macOS / Linux / Windows)
param([string]$ApiBaseUrls = '')
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)

if (-not (Test-Path 'windows\core\huanyou-core.exe')) {
  throw 'Missing windows\core\huanyou-core.exe. Run: bash ../mobile/build.sh windows'
}

$defines = @()
if ($ApiBaseUrls) { $defines += "--dart-define=API_BASE_URLS=$ApiBaseUrls" }
flutter build windows --release --obfuscate --split-debug-info=build\symbols @defines
if ($LASTEXITCODE -ne 0) { throw 'flutter build windows failed' }

$ver = (Select-String -Path pubspec.yaml -Pattern '^version:\s*(\S+)').Matches[0].Groups[1].Value
$name, $build = $ver -split '\+'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'x64' } # Flutter builds for the host CPU
$out = "build\dist\huanyou-windows-$name-$build-$arch.zip"
New-Item -ItemType Directory -Force -Path build\dist | Out-Null
if (Test-Path $out) { Remove-Item $out }
# The zip holds one folder (the app name) with Huanyou.exe, huanyou-core.exe, the DLLs and data\.
$stage = 'build\dist\stage'
if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
New-Item -ItemType Directory -Force -Path $stage | Out-Null
# The admin panel reads version and CPU from Huanyou.exe, so the name is informational.
Copy-Item -Recurse "build\windows\$arch\runner\Release" (Join-Path $stage ([string][char]0x73AF + [char]0x6E38))
Compress-Archive -Path "$stage\*" -DestinationPath $out
Remove-Item -Recurse -Force $stage
Write-Host "-> $out (build $build)"
