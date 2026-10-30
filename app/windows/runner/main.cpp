#include <flutter/dart_project.h>
#include <flutter/flutter_view_controller.h>
#include <windows.h>

#include "flutter_window.h"
#include "utils.h"

int APIENTRY wWinMain(_In_ HINSTANCE instance, _In_opt_ HINSTANCE prev,
                      _In_ wchar_t *command_line, _In_ int show_command) {
  // One instance only: two copies would each start a VPN core and fight over
  // the system proxy. A second launch brings the running window forward.
  HANDLE single = ::CreateMutexW(nullptr, TRUE, L"Local\\HuanyouVpnApp");
  if (single != nullptr && ::GetLastError() == ERROR_ALREADY_EXISTS) {
    if (HWND existing = ::FindWindowW(L"FLUTTER_RUNNER_WIN32_WINDOW", L"\u73af\u6e38")) {
      ::ShowWindow(existing, SW_RESTORE);
      ::SetForegroundWindow(existing);
    }
    return EXIT_SUCCESS;
  }

  // Attach to console when present (e.g., 'flutter run') or create a
  // new console when running with a debugger.
  if (!::AttachConsole(ATTACH_PARENT_PROCESS) && ::IsDebuggerPresent()) {
    CreateAndAttachConsole();
  }

  // Initialize COM, so that it is available for use in the library and/or
  // plugins.
  ::CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);

  flutter::DartProject project(L"data");

  std::vector<std::string> command_line_arguments =
      GetCommandLineArguments();

  project.set_dart_entrypoint_arguments(std::move(command_line_arguments));

  FlutterWindow window(project);
  // Phone-first UI: one fixed size (logical pixels, DPI-scaled); keep in
  // sync with lib/widgets/desktop_frame.dart.
  Win32Window::Point origin(80, 40);
  Win32Window::Size size(420, 820);
  if (!window.Create(L"\u73af\u6e38", origin, size)) {  // 环游
    return EXIT_FAILURE;
  }
  window.SetQuitOnClose(true);

  ::MSG msg;
  while (::GetMessage(&msg, nullptr, 0, 0)) {
    ::TranslateMessage(&msg);
    ::DispatchMessage(&msg);
  }

  ::CoUninitialize();
  return EXIT_SUCCESS;
}
