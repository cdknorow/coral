$ErrorActionPreference = 'Continue'
$exe = 'C:\walk\coral-app.exe'
New-Item -ItemType Directory -Force C:\walk | Out-Null
Push-Location coral-go
$env:CGO_ENABLED = '1'; $env:CC = 'gcc'
go build -tags 'webview,fts5,dev' -ldflags '-H windowsgui' -o $exe ./cmd/coral-app
if ($LASTEXITCODE -ne 0) { throw 'coral-app did not build' }
Pop-Location
$env:CORAL_DATA_DIR = 'C:\walk\data'; New-Item -ItemType Directory -Force $env:CORAL_DATA_DIR | Out-Null
$env:CORAL_PROBE_NO_UA = '1'
function Try-Args($label, $args2, $port) {
  $listener = New-Object System.Net.HttpListener
  $listener.Prefixes.Add("http://127.0.0.1:$port/")
  $listener.Start()
  if ($args2) { $env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = $args2 } else { Remove-Item Env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS -ErrorAction SilentlyContinue }
  $p = Start-Process -FilePath $exe -ArgumentList '--url', "http://127.0.0.1:$port/" -PassThru
  $task = $listener.GetContextAsync(); $ua = '(none)'
  if ($task.Wait(40000)) { $ctx = $task.Result; $ua = $ctx.Request.UserAgent; $b = [Text.Encoding]::UTF8.GetBytes('<html><body>ok</body></html>'); $ctx.Response.OutputStream.Write($b,0,$b.Length); $ctx.Response.Close() }
  Write-Host "VARIANT $label => UA: $ua"
  Get-Process -Name 'coral-app','msedgewebview2' -ErrorAction SilentlyContinue | Stop-Process -Force
  $listener.Stop(); Start-Sleep -Seconds 2
}
Try-Args 'none' '' 18991
Try-Args 'single token' '--user-agent=CoralApp/1' 18992
Try-Args 'quoted with spaces' '--user-agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) CoralApp/1"' 18993
Try-Args 'plain switch mute-audio (is the env var read at all)' '--mute-audio' 18994
Try-Args 'enable-features as a control' '--user-agent=Coral_App --disable-features=msWebView2EnableDraggableRegionSupport' 18995
