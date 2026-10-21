# Install mcli from GitHub Releases on Windows.
#
#   irm https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.ps1 | iex
#
# Environment:
#   MCLI_VERSION        release tag to install, e.g. v0.1.0 (default: latest)
#   MCLI_INSTALL_DIR    target directory (default: %LOCALAPPDATA%\Programs\mcli)
#   MCLI_REPO           GitHub repository (default: ZeroPointSix/metamcp-cli)
#   MCLI_DOWNLOAD_BASE  override the asset base URL (mirrors, tests)
#   MCLI_NO_PATH        set to 1 to leave the user PATH unchanged
#
# The archive is verified against the release's checksums.txt (SHA-256)
# before anything is installed. No administrator rights are needed.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

function Say($msg) { [Console]::Error.WriteLine("mcli-install: $msg") }
function Die($msg) { Say "error: $msg"; exit 1 }

$repo = if ($env:MCLI_REPO) { $env:MCLI_REPO } else { 'ZeroPointSix/metamcp-cli' }
$version = if ($env:MCLI_VERSION) { $env:MCLI_VERSION } else { 'latest' }
$installDir = if ($env:MCLI_INSTALL_DIR) { $env:MCLI_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\mcli' }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { Die "unsupported architecture $($env:PROCESSOR_ARCHITECTURE)" }
}
$archive = "mcli_windows_$arch.zip"

if ($env:MCLI_DOWNLOAD_BASE) { $base = $env:MCLI_DOWNLOAD_BASE }
elseif ($version -eq 'latest') { $base = "https://github.com/$repo/releases/latest/download" }
else { $base = "https://github.com/$repo/releases/download/$version" }

[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("mcli-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Say "downloading $archive ($version)"
  try {
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$archive" -OutFile (Join-Path $tmp $archive)
    Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')
  } catch { Die "download failed from ${base}: $($_.Exception.Message)" }

  $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { ($_ -split '\s+')[1] -in @($archive, "*$archive") } | Select-Object -First 1
  if (-not $line) { Die "$archive is not listed in checksums.txt" }
  $want = ($line -split '\s+')[0].ToLower()
  $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $archive)).Hash.ToLower()
  if ($want -ne $got) { Die "checksum mismatch for $archive (expected $want, got $got)" }

  $extract = Join-Path $tmp 'x'
  Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $extract
  $exe = Join-Path $extract 'mcli.exe'
  if (-not (Test-Path $exe)) { Die "mcli.exe not found in $archive" }

  New-Item -ItemType Directory -Force -Path $installDir | Out-Null
  $target = Join-Path $installDir 'mcli.exe'
  # A running daemon locks mcli.exe; Windows still allows renaming it away.
  if (Test-Path $target) {
    $old = "$target.old"
    Remove-Item -Force $old -ErrorAction SilentlyContinue
    Move-Item -Force $target $old
  }
  Copy-Item $exe $target
  Remove-Item -Force "$target.old" -ErrorAction SilentlyContinue

  $installed = & $target --version 2>$null
  Say "installed $installed to $target"

  if ($env:MCLI_NO_PATH -ne '1') {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (($userPath -split ';') -contains $installDir)) {
      $newPath = if ($userPath) { "$userPath;$installDir" } else { $installDir }
      [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
      Say "added $installDir to the user PATH (open a new terminal to use it)"
    }
    if (-not (($env:Path -split ';') -contains $installDir)) { $env:Path = "$env:Path;$installDir" }
  }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
