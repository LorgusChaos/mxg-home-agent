# Puts the Telegram bot token onto the box without it appearing in chat or in a command line.
# The agent imports /data/mxg/token.txt into config.json at start and deletes the file.
# Run:  powershell -ExecutionPolicy Bypass -File "scripts\set_token.ps1"
$ErrorActionPreference = 'Stop'
# box address: $env:MXG_BOX, else box.txt in the project root
$T = $env:MXG_BOX
$boxFile = Join-Path $PSScriptRoot '..\box.txt'
if (-not $T -and (Test-Path $boxFile)) { $T = (Get-Content $boxFile -TotalCount 1).Trim() }
if (-not $T) { Write-Host 'Box address not set: set MXG_BOX or create box.txt (IP:5555)'; exit 2 }
$sec = Read-Host 'Token from @BotFather' -AsSecureString
$token = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec)).Trim()
if ($token -notmatch '^\d{6,}:[A-Za-z0-9_-]{30,}$') { Write-Host 'This does not look like a bot token.'; exit 1 }
$tmp = Join-Path $env:TEMP ('mxg_token_' + [guid]::NewGuid().ToString('N') + '.txt')
try {
    [IO.File]::WriteAllText($tmp, $token)
    adb -s $T push $tmp /data/mxg/token.txt | Out-Null
    adb -s $T shell 'chmod 600 /data/mxg/token.txt; sh /data/mxg/stop.sh; sleep 2; sh /data/mxg/start.sh'
    Start-Sleep 8
    adb -s $T shell 'grep -E "token imported|not paired|telegram" /data/mxg/mxg.log | tail -3'
    Write-Host 'Done. Now send the bot:  /pair <code>  (the code is in the line above).'
} finally {
    Remove-Item $tmp -ErrorAction SilentlyContinue
}
