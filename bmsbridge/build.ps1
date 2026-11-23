# Builds bmsbridge.apk without Gradle: aapt2 (manifest) -> javac -> d8 -> zip -> zipalign -> apksigner.
# Needs JDK 17 and the Android SDK (build-tools 35+, platform android-36). Output: build\bmsbridge.apk
$ErrorActionPreference = 'Continue' # native tools print notes to stderr; failures are caught via $LASTEXITCODE
$root = $PSScriptRoot
$sdk = "$env:LOCALAPPDATA\Android\Sdk"
$bt = Get-ChildItem "$sdk\build-tools" | Where-Object { $_.Name -notmatch 'rc' } | Sort-Object Name -Descending | Select-Object -First 1
$bt = $bt.FullName
$jar = (Get-ChildItem "$sdk\platforms" | Sort-Object Name -Descending | Select-Object -First 1).FullName + '\android.jar'
$out = Join-Path $root 'build'
Remove-Item $out -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force "$out\classes", "$out\dex" | Out-Null

Write-Host "build-tools: $bt"; Write-Host "android.jar: $jar"

& "$bt\aapt2.exe" link --manifest "$root\AndroidManifest.xml" -I $jar -o "$out\base.apk" --min-sdk-version 21 --target-sdk-version 21
if ($LASTEXITCODE) { throw 'aapt2 failed' }

$src = Get-ChildItem "$root\src" -Recurse -Filter *.java | ForEach-Object { $_.FullName }
& javac -source 8 -target 8 -bootclasspath $jar -encoding UTF-8 -Xlint:-options -nowarn -d "$out\classes" @src 2>&1 | Where-Object { "$_" -notmatch '^Note:' }
if ($LASTEXITCODE) { throw 'javac failed' }

$classes = Get-ChildItem "$out\classes" -Recurse -Filter *.class | ForEach-Object { $_.FullName }
& "$bt\d8.bat" --release --min-api 21 --lib $jar --output "$out\dex" @classes
if ($LASTEXITCODE) { throw 'd8 failed' }

Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip = [System.IO.Compression.ZipFile]::Open("$out\base.apk", 'Update')
[System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, "$out\dex\classes.dex", 'classes.dex') | Out-Null
$zip.Dispose()

& "$bt\zipalign.exe" -f 4 "$out\base.apk" "$out\aligned.apk"
if ($LASTEXITCODE) { throw 'zipalign failed' }

# Signing key: bmsbridge.keystore + its password in keystore.pass (both local only, never committed).
# First build creates both with a random password. Keep them: an installed bridge can only be updated with the same key.
$ks = Join-Path $root 'bmsbridge.keystore'
$pf = Join-Path $root 'keystore.pass'
if (-not (Test-Path $pf)) {
    if (Test-Path $ks) { throw "bmsbridge.keystore exists but keystore.pass is missing: put its password into $pf" }
    $bytes = New-Object byte[] 18; [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    [IO.File]::WriteAllText($pf, [Convert]::ToBase64String($bytes).Replace('/', 'x').Replace('+', 'y'))
}
$pw = (Get-Content $pf -TotalCount 1).Trim()
if (-not (Test-Path $ks)) {
    & keytool -genkeypair -keystore $ks -storepass $pw -keypass $pw -alias mxg -keyalg RSA -keysize 2048 -validity 10000 -dname 'CN=mxg bmsbridge' | Out-Null
}
& "$bt\apksigner.bat" sign --ks $ks --ks-pass "pass:$pw" --key-pass "pass:$pw" --out "$out\bmsbridge.apk" "$out\aligned.apk"
if ($LASTEXITCODE) { throw 'apksigner failed' }
& "$bt\apksigner.bat" verify --print-certs "$out\bmsbridge.apk" | Select-Object -First 2
Get-Item "$out\bmsbridge.apk" | Select-Object Name, Length
