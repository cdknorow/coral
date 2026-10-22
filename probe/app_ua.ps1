$ErrorActionPreference = 'Continue'
$exe = 'C:\walk\coral-app.exe'
New-Item -ItemType Directory -Force C:\walk | Out-Null
Push-Location coral-go
$env:CGO_ENABLED = '1'; $env:CC = 'gcc'
go build -tags 'webview,fts5,dev' -ldflags '-H windowsgui' -o $exe ./cmd/coral-app
if ($LASTEXITCODE -ne 0) { throw 'coral-app did not build' }
Pop-Location
# a server that records the User-Agent of every request
$listener = New-Object System.Net.HttpListener
$listener.Prefixes.Add('http://127.0.0.1:18999/')
$listener.Start()
$env:CORAL_DATA_DIR = 'C:\walk\data'; New-Item -ItemType Directory -Force $env:CORAL_DATA_DIR | Out-Null
Remove-Item Env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS -ErrorAction SilentlyContinue
$p = Start-Process -FilePath $exe -ArgumentList '--url', 'http://127.0.0.1:18999/' -PassThru
$deadline = (Get-Date).AddSeconds(40); $seen = @()
$task = $listener.GetContextAsync()
while ((Get-Date) -lt $deadline -and $seen.Count -lt 2) {
  if ($task.Wait(2000)) {
    $ctx = $task.Result
    $seen += $ctx.Request.UserAgent
    Write-Host "request $($ctx.Request.Url.AbsolutePath) UA: $($ctx.Request.UserAgent)"
    $bytes = [Text.Encoding]::UTF8.GetBytes('<html><body>ok</body></html>')
    $ctx.Response.OutputStream.Write($bytes, 0, $bytes.Length); $ctx.Response.Close()
    $task = $listener.GetContextAsync()
  }
}
Write-Host "requests seen: $($seen.Count); app running: $(-not $p.HasExited)"
Write-Host "marker present in every request: $((($seen | Where-Object { $_ -notmatch 'CoralApp/' }).Count) -eq 0)"
Write-Host "looks like Edge: $((($seen | Where-Object { $_ -notmatch ' Edg/' }).Count) -eq 0)"
Get-Process -Name 'coral-app','msedgewebview2' -ErrorAction SilentlyContinue | Stop-Process -Force
$listener.Stop()
