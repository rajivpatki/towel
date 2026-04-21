#define MyAppName "Towel"
#define MyAppPublisher "Jodaro"
#define MyAppVersion GetEnv("TOWEL_VERSION")
#if MyAppVersion == ""
  #define MyAppVersion "0.1.0"
#endif

[Setup]
AppId={{B9C19F47-2CB3-4F80-8A41-DA7E9E915421}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={autopf}\Towel
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=admin
OutputDir=..\dist
OutputBaseFilename=towel-windows-{#MyAppVersion}
UninstallDisplayIcon={app}\launch_towel.bat

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; Flags: unchecked

[Files]
Source: "..\..\install_windows.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\install.yml"; DestDir: "{app}"; Flags: ignoreversion
Source: "launch_towel.bat"; DestDir: "{app}"; Flags: ignoreversion
Source: "stop_towel.bat"; DestDir: "{app}"; Flags: ignoreversion
Source: "stop_towel.ps1"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Towel"; Filename: "{app}\launch_towel.bat"; WorkingDir: "{app}"
Name: "{autoprograms}\Towel Stop"; Filename: "{app}\stop_towel.bat"; WorkingDir: "{app}"
Name: "{autodesktop}\Towel"; Filename: "{app}\launch_towel.bat"; WorkingDir: "{app}"; Tasks: desktopicon

[Run]
Filename: "{app}\launch_towel.bat"; Description: "Launch Towel now"; Flags: nowait postinstall skipifsilent
