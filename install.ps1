# Installs the latest onvif-cam-sim release on Windows (64-bit).
#   irm https://raw.githubusercontent.com/Assaf-Coda/onvif-cam-sim/main/install.ps1 | iex
# Installs to %LOCALAPPDATA%\onvif-cam-sim and adds that folder to the user PATH.
$ErrorActionPreference = "Stop"
$repo = "Assaf-Coda/onvif-cam-sim"
$version = if ($env:VERSION) { $env:VERSION } else { (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name }
$name = "onvif-cam-sim_${version}_windows_amd64"
$base = "https://github.com/$repo/releases/download/$version"
$tmp = Join-Path $env:TEMP "onvif-cam-sim-install"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
Write-Host "downloading $name.zip"
Invoke-WebRequest "$base/$name.zip" -OutFile "$tmp\$name.zip"
Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt"
$expected = (Select-String -Path "$tmp\checksums.txt" -Pattern " $name.zip$").Line.Split(" ")[0]
$actual = (Get-FileHash "$tmp\$name.zip" -Algorithm SHA256).Hash.ToLower()
if ($expected -ne $actual) { throw "checksum mismatch for $name.zip" }
$dest = Join-Path $env:LOCALAPPDATA "onvif-cam-sim"
Expand-Archive "$tmp\$name.zip" -DestinationPath $tmp -Force
New-Item -ItemType Directory -Force -Path $dest | Out-Null
Copy-Item "$tmp\$name\*" $dest -Force
Remove-Item $tmp -Recurse -Force
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$dest*") {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$dest", "User")
  $env:Path += ";$dest"
  Write-Host "added $dest to the user PATH (open a new terminal to pick it up)"
}
Write-Host "installed $version to $dest\onvif-cam-sim.exe"
if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue)) { Write-Host "note: ffmpeg not found; run 'winget install ffmpeg' before starting the simulator" }
Write-Host "next: copy $dest\config.example.yaml to config.yaml, edit it, then run onvif-cam-sim -check"
