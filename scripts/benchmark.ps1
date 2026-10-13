# Benchmark srcinsight - native Windows (PowerShell 5.1+ / pwsh).
# Mirrors scripts/benchmark.sh but with Windows-native measurement:
#   wall  -> .NET Stopwatch (vs /usr/bin/time)
#   peak  -> Process.PeakWorkingSet64 (vs %MKB maxrss)
#   env   -> CIM Win32_* (vs lscpu//proc/meminfo)
#   copy  -> robocopy (vs cp -r)
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts/benchmark.ps1 -Root ./projectTest/zod-main -Runs 3 -NoCopy
#   powershell -ExecutionPolicy Bypass -File scripts/benchmark.ps1 -Root ./projectTest/zod-main -Runs 1
#
# Notes:
#   - Any non-zero exit from srcinsight stops the run (fail-fast). If the
#     stderr points at internal/ or services/ (panic/goroutine), it is
#     reported as SEVERE and the run stops immediately.

param(
  [string]$Root = "./projectTest/zod-main",
  [string]$Bin = "./srcinsight.exe",
  [string]$Work = "",
  [int]$Runs = 3,
  [switch]$NoCopy,
  [switch]$KeepCopy
)

$ErrorActionPreference = 'Stop'

function Format-Args([string[]]$ArgList) {
  $out = @()
  foreach ($a in $ArgList) {
    if ($a -match '[\s"]') { $out += '"' + ($a -replace '"', '\"') + '"' }
    else { $out += $a }
  }
  return ($out -join ' ')
}

# Run an exe, return @{ WallSec; PeakMB; Stdout; Stderr; ExitCode }.
# Wall via Stopwatch; peak RSS via live WorkingSet64 sampling (every 25ms)
# maxed with the PeakWorkingSet64 API (which has been observed to read 0
# after exit on some .NET/Win builds, so sampling is the primary source).
# Throws (stops benchmark) on non-zero exit; flags internal/services errors.
function Invoke-TimedNative([string]$Exe, [string[]]$ArgList) {
  $argStr = Format-Args $ArgList
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = $Exe
  $psi.Arguments = $argStr
  $psi.UseShellExecute = $false
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  $psi.CreateNoWindow = $true
  $p = New-Object System.Diagnostics.Process
  $p.StartInfo = $psi
  $sw = [System.Diagnostics.Stopwatch]::StartNew()
  [void]$p.Start()
  # Sample live WorkingSet64: the primary peak source (PeakWorkingSet64 and
  # Start-Process -PassThru ExitCode both proved unreliable in this env).
  $maxWs = 0
  while (-not $p.HasExited) {
    try {
      $p.Refresh()
      if ($p.WorkingSet64 -gt $maxWs) { $maxWs = $p.WorkingSet64 }
    } catch {}
    Start-Sleep -Milliseconds 25
  }
  $p.WaitForExit()
  $sw.Stop()
  $wall = $sw.Elapsed.TotalSeconds
  # Process already exited: buffered pipe content is safe to drain now.
  $stdout = $p.StandardOutput.ReadToEnd()
  $stderr = $p.StandardError.ReadToEnd()
  $peakApi = 0
  try { $peakApi = $p.PeakWorkingSet64 } catch { $peakApi = 0 }
  if ($peakApi -gt $maxWs) { $maxWs = $peakApi }
  $peakMB = [math]::Round($maxWs / 1MB, 1)
  $code = $p.ExitCode
  try { $p.Close() } catch {}
  if ($null -eq $stdout) { $stdout = '' } else { $stdout = $stdout.Trim() }
  if ($null -eq $stderr) { $stderr = '' } else { $stderr = $stderr.Trim() }
  if ($code -ne 0) {
    Write-Host ("EXIT CODE {0}: {1} {2}" -f $code, $Exe, $argStr)
    if ($stdout) { Write-Host ("stdout: {0}" -f $stdout) }
    if ($stderr) { Write-Host ("stderr: {0}" -f $stderr) }
    $severe = ($stderr -match 'panic|goroutine|fatal|internal/|services/')
    if ($severe) { Write-Host "STOP: SEVERE error from internal/ or services/ - benchmark halted." }
    throw ("Command failed with exit code {0}: {1} {2}" -f $code, $Exe, $argStr)
  }
  return @{ WallSec = $wall; PeakMB = $peakMB; Stdout = $stdout; Stderr = $stderr }
}

function Invoke-Sqlite([string]$Db, [string]$Sql) {
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = "sqlite3"
  $psi.Arguments = '"' + $Db + '" "' + ($Sql -replace '"', '""') + '"'
  $psi.UseShellExecute = $false
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  $psi.CreateNoWindow = $true
  $p = [System.Diagnostics.Process]::Start($psi)
  $out = $p.StandardOutput.ReadToEnd()
  $err = $p.StandardError.ReadToEnd()
  $p.WaitForExit()
  if ($p.ExitCode -ne 0) { throw ("sqlite3 failed: " + $err) }
  $p.Close()
  return $out.Trim()
}

# Resolve paths
$Root = (Resolve-Path $Root).Path
if ($Bin -notmatch '^[A-Za-z]:|^\\\\') {
  $BinAbs = Join-Path (Get-Location).Path $Bin
} else { $BinAbs = $Bin }
if ([string]::IsNullOrEmpty($Work)) {
  $Work = Join-Path $env:TEMP ("wk-" + (Split-Path $Root -Leaf) + "-" + $PID)
}
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

#  Build binary if missing 
# gcc fallback: tree-sitter needs CGO, so ensure a gcc is reachable.
# If plain `gcc` is not on PATH, try C:\msys64\ucrt64\bin before building.
try { [void](Get-Command gcc -ErrorAction Stop) }
catch {
  $gccFallback = 'C:\msys64\ucrt64\bin'
  if (Test-Path (Join-Path $gccFallback 'gcc.exe')) {
    Write-Host ("gcc not on PATH, prepending fallback {0}" -f $gccFallback)
    $env:PATH = $gccFallback + ';' + $env:PATH
  }
}
if (-not (Test-Path $BinAbs)) {
  Write-Host "Building $BinAbs ..."
  & go build -o $BinAbs ./cmd/srcinsight
  if ($LASTEXITCODE -ne 0) { throw "go build failed" }
}

# Environment
Write-Host "=== Environment (Windows-native) ==="
Write-Host ("Date: {0:yyyy-MM-dd HH:mm:ss}" -f (Get-Date))
try {
  $cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
  $threads = (Get-CimInstance Win32_Processor | Measure-Object -Property NumberOfLogicalProcessors -Sum).Sum
  Write-Host ("CPU: {0} ({1} logical threads)" -f $cpu.Name.Trim(), $threads)
  $ramGB = [math]::Round((Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory / 1GB, 1)
  Write-Host ("RAM: {0} GiB" -f $ramGB)
  $os = Get-CimInstance Win32_OperatingSystem
  Write-Host ("OS: {0} {1} (build {2})" -f $os.Caption, $os.OSArchitecture, $os.BuildNumber)
} catch { Write-Host ("env-warn: {0}" -f $_.Exception.Message) }
try {
  $vol = (Get-Item $Root).PSDrive.Name
  $drv = Get-PSDrive $vol
  Write-Host ("Disk {0}: used {1:N1} GB / free {2:N1} GB" -f $vol, ($drv.Used/1GB), ($drv.Free/1GB))
} catch { Write-Host ("disk-warn: {0}" -f $_.Exception.Message) }
& go version
try {
  $gccLine = (& gcc --version 2>&1 | Select-Object -First 1).ToString().Trim()
} catch { $gccLine = $null }
if ([string]::IsNullOrWhiteSpace($gccLine)) {
  $gccFb = 'C:\msys64\ucrt64\bin\gcc.exe'
  if (Test-Path $gccFb) {
    try { $gccLine = ((& $gccFb --version 2>&1 | Select-Object -First 1).ToString().Trim() + ' (fallback)') } catch { $gccLine = 'n/a' }
  } else { $gccLine = 'n/a' }
}
Write-Host ("gcc: {0}" -f $gccLine)
Write-Host ("CGO_ENABLED: {0}" -f $env:CGO_ENABLED)
Write-Host ("Binary: {0} ({1:N1} MB)" -f $BinAbs, ((Get-Item $BinAbs).Length / 1MB))
Write-Host ("Root: {0}" -f $Root)
Write-Host ("Work: {0} (NoCopy={1})" -f $Work, $NoCopy.IsPresent)
Write-Host ("Runs: {0}" -f $Runs)
Write-Host ""

# Prepare bench root
if (-not $NoCopy) {
  Write-Host "Copying $Root -> $Work (robocopy, excl .srcinsight/.git) ..."
  New-Item -ItemType Directory -Force -Path $Work | Out-Null
  robocopy $Root $Work /E /XD .srcinsight .git /NFL /NDL /NJH /NJS | Out-Null
  if ($LASTEXITCODE -ge 8) { throw "robocopy failed with code $LASTEXITCODE" }
  $BenchRoot = $Work
} else {
  Write-Host "No-copy: benchmarking directly on $Root ..."
  $BenchRoot = $Root
  if (Test-Path (Join-Path $BenchRoot ".srcinsight")) {
    Remove-Item -Recurse -Force (Join-Path $BenchRoot ".srcinsight")
  }
}
$DbPath = Join-Path $BenchRoot ".srcinsight/srcinsight.db"

# Corpus stats
$allFiles = Get-ChildItem -Path $BenchRoot -Recurse -File -ErrorAction SilentlyContinue
$srcFiles = @($allFiles | Where-Object { $_.Extension -in '.ts','.tsx','.mts','.cts','.py' })
$tsFiles = @($allFiles | Where-Object { $_.Extension -eq '.ts' })
$corpusMB = [math]::Round(($allFiles | Measure-Object -Property Length -Sum).Sum / 1MB, 1)
$tsLines = 0
foreach ($f in $tsFiles) {
  try { foreach ($l in [System.IO.File]::ReadLines($f.FullName)) { $tsLines++ } }
  catch { Write-Host ("linecount-warn: {0}" -f $f.FullName) }
}
Write-Host ("Corpus size: ~{0} MB (all files under root)" -f $corpusMB)
Write-Host ("Source files (ts/tsx/mts/cts/py): {0}" -f $srcFiles.Count)
Write-Host ("TS lines (approx): ~{0}k" -f [math]::Round($tsLines / 1000))
Write-Host ""

# Fresh index
$freshWalls = @()
for ($i = 1; $i -le $Runs; $i++) {
  if (Test-Path (Join-Path $BenchRoot ".srcinsight")) {
    Remove-Item -Recurse -Force (Join-Path $BenchRoot ".srcinsight")
  }
  Write-Host "--- Fresh index run $i ---"
  $r = Invoke-TimedNative $BinAbs @('index', '--root', $BenchRoot)
  Write-Host $r.Stdout
  Write-Host ("wall={0:N2}s maxrss={1}MB" -f $r.WallSec, $r.PeakMB)
  $freshWalls += $r.WallSec
}
$avgWall = ($freshWalls | Measure-Object -Average).Average
$minWall = ($freshWalls | Measure-Object -Minimum).Minimum
$maxWall = ($freshWalls | Measure-Object -Maximum).Maximum
$spreadPct = 0
if ($avgWall -gt 0) { $spreadPct = (($maxWall - $minWall) / $avgWall) * 100 }
Write-Host ("Fresh avg: {0:N2}s over {1} runs (min {2:N2}s / max {3:N2}s, spread {4:N1}% tolerance band for busy CPU)" -f $avgWall, $Runs, $minWall, $maxWall, $spreadPct)
Write-Host ""

# Second index no changes
Write-Host "--- Second index no changes ---"
$r = Invoke-TimedNative $BinAbs @('index', '--root', $BenchRoot)
Write-Host $r.Stdout
Write-Host ("wall={0:N2}s maxrss={1}MB" -f $r.WallSec, $r.PeakMB)
Write-Host ""

# DB stats (never fail the whole bench)
try {
  if (Test-Path $DbPath) {
    Write-Host "--- DB stats ---"
    Write-Host ("DB size: {0:N1} MB" -f ((Get-Item $DbPath).Length / 1MB))
    try {
      Write-Host (Invoke-Sqlite $DbPath "SELECT 'entries ' || COUNT(*) FROM entries;")
      Write-Host (Invoke-Sqlite $DbPath "SELECT 'links ' || COUNT(*) FROM links;")
      try { Write-Host (Invoke-Sqlite $DbPath "SELECT 'loose ' || COUNT(*) FROM loose_ends;") } catch { Write-Host "loose: n/a" }
      try { Write-Host (Invoke-Sqlite $DbPath "SELECT 'imports ' || COUNT(*) FROM brought_in;") } catch { Write-Host "imports: n/a (brought_in missing)" }
      Write-Host " entries by kind:"
      try { Write-Host (Invoke-Sqlite $DbPath "SELECT kind || ' ' || COUNT(*) FROM entries GROUP BY kind ORDER BY COUNT(*) DESC;") } catch { Write-Host "  n/a" }
      Write-Host " links by kind:"
      try { Write-Host (Invoke-Sqlite $DbPath "SELECT kind || ' ' || COUNT(*) FROM links GROUP BY kind ORDER BY COUNT(*) DESC;") } catch { Write-Host "  n/a" }
      try { Write-Host (Invoke-Sqlite $DbPath "SELECT 'cond_links ' || COUNT(*) FROM links WHERE cond != '';") } catch { Write-Host "cond_links: n/a (no cond column)" }
      try { Write-Host (Invoke-Sqlite $DbPath "SELECT 'recv_binds ' || COUNT(*) FROM recv_binds;") } catch { Write-Host "recv_binds: n/a" }
    } catch { Write-Host ("sqlite-warn: {0}" -f $_.Exception.Message) }
  } else { Write-Host "DB not found, skipping stats" }
} catch { Write-Host ("dbstats-warn: {0}" -f $_.Exception.Message) }
Write-Host ""

# Sync 1 changed file
$target = $null
try {
  $cand = Get-ChildItem -Path (Join-Path $BenchRoot "packages") -Recurse -Filter *.ts -File -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($cand) { $target = $cand.FullName }
  else { $target = (Get-ChildItem -Path $BenchRoot -Recurse -Filter *.ts -File -ErrorAction SilentlyContinue | Select-Object -First 1).FullName }
} catch { $target = $null }
Write-Host ("Target for sync: {0}" -f $target)
if ($target -and (Test-Path $target)) {
  $orig = [System.IO.File]::ReadAllText($target)
  [System.IO.File]::AppendAllText($target, "`n// bench touch " + (Get-Date -Format o) + "`n")
  Write-Host "--- Sync 1 changed file ---"
  $r = Invoke-TimedNative $BinAbs @('sync', '--root', $BenchRoot)
  Write-Host $r.Stdout
  Write-Host ("wall={0:N2}s maxrss={1}MB" -f $r.WallSec, $r.PeakMB)
  Write-Host "--- Sync no-op ---"
  $r = Invoke-TimedNative $BinAbs @('sync', '--root', $BenchRoot)
  Write-Host $r.Stdout
  Write-Host ("wall={0:N2}s maxrss={1}MB" -f $r.WallSec, $r.PeakMB)
  [System.IO.File]::WriteAllText($target, $orig)
  $r = Invoke-TimedNative $BinAbs @('sync', '--root', $BenchRoot)
  Write-Host ("revert re-sync: {0}" -f $r.Stdout)
  Write-Host ("wall={0:N2}s maxrss={1}MB" -f $r.WallSec, $r.PeakMB)
} else { Write-Host "No TS file found for sync test" }
Write-Host ""

# Incremental hinted vs full
Write-Host "--- Incremental hinted vs full (benchincremental) ---"
Push-Location $RepoRoot
try {
  & go run ./cmd/benchincremental $BenchRoot
  if ($LASTEXITCODE -ne 0) { throw "benchincremental failed with code $LASTEXITCODE" }
} finally { Pop-Location }
Write-Host ""

# Graph / Search latency (direct vault)
Write-Host "--- Graph / Search latency (benchhelper, direct vault) ---"
Push-Location $RepoRoot
try {
  & go run ./cmd/benchhelper $BenchRoot
  if ($LASTEXITCODE -ne 0) { throw "benchhelper failed with code $LASTEXITCODE" }
} finally { Pop-Location }
Write-Host ""

# Serve idle RSS (native process, JSON-RPC over stdio) 
Write-Host "--- Idle serve RSS ---"
try {
  $psi = New-Object System.Diagnostics.ProcessStartInfo
  $psi.FileName = $BinAbs
  $psi.Arguments = 'serve --root "' + $BenchRoot + '"'
  $psi.UseShellExecute = $false
  $psi.RedirectStandardInput = $true
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  $psi.CreateNoWindow = $true
  $srv = New-Object System.Diagnostics.Process
  $srv.StartInfo = $psi
  [void]$srv.Start()
  Start-Sleep -Seconds 4
  if ($srv.HasExited) {
    Write-Host "serve failed to start"
    Write-Host $srv.StandardError.ReadToEnd()
  } else {
    $srv.Refresh()
    Write-Host ("serve idle RSS: {0:N1} MB (working set)" -f ($srv.WorkingSet64 / 1MB))
    try {
      $srv.StandardInput.WriteLine('{"jsonrpc":"2.0","id":1,"method":"search","params":{"query":"bootstrapApplication"}}')
      $t1 = $srv.StandardOutput.ReadLineAsync()
      if ($t1.Wait(8000)) { Write-Host ("search reply bytes: {0}" -f $t1.Result.Length) }
      else { Write-Host "search reply: timeout" }
      Start-Sleep -Seconds 1
      $srv.Refresh()
      Write-Host ("serve RSS after search: {0:N1} MB" -f ($srv.WorkingSet64 / 1MB))
    } catch { Write-Host ("serve-search-warn: {0}" -f $_.Exception.Message) }
    try {
      $fid = $null
      try { $fid = (Invoke-Sqlite $DbPath "SELECT id FROM entries LIMIT 1;").Trim() } catch { $fid = $null }
      if ($fid) {
        $srv.StandardInput.WriteLine(('{"jsonrpc":"2.0","id":2,"method":"graph","params":{"id":"' + $fid + '"}}'))
        $t2 = $srv.StandardOutput.ReadLineAsync()
        if ($t2.Wait(8000)) { Write-Host ("graph reply bytes: {0}" -f $t2.Result.Length) }
        else { Write-Host "graph reply: timeout" }
        Start-Sleep -Seconds 1
        $srv.Refresh()
        Write-Host ("serve RSS after graph: {0:N1} MB" -f ($srv.WorkingSet64 / 1MB))
      } else { Write-Host "graph skipped (no fid)" }
    } catch { Write-Host ("serve-graph-warn: {0}" -f $_.Exception.Message) }
    try {
      $srv.StandardInput.WriteLine('{"jsonrpc":"2.0","id":99,"method":"shutdown"}')
      if (-not $srv.WaitForExit(8000)) { $srv.Kill() }
      Write-Host "serve exited"
    } catch { Write-Host ("serve-stop-warn: {0}" -f $_.Exception.Message) }
  }
  try { if (-not $srv.HasExited) { $srv.Kill() } } catch {}
} catch { Write-Host ("serve-warn: {0}" -f $_.Exception.Message) }
Write-Host ""

# Cleanup
if ($NoCopy) {
  # Leave the fixture pristine: v7 doc removes .srcinsight afterwards.
  if (Test-Path (Join-Path $BenchRoot ".srcinsight")) {
    Remove-Item -Recurse -Force (Join-Path $BenchRoot ".srcinsight")
    Write-Host "Cleaned $BenchRoot/.srcinsight (fixture pristine)."
  }
} elseif (-not $KeepCopy) {
  Write-Host "Work dir kept for inspection: $Work (use -KeepCopy to retain explicitly)"
}

Write-Host "Done."
