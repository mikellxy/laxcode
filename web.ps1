[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$WebDir = Join-Path $RepoRoot "web"
$BinDir = Join-Path $RepoRoot "bin"
$BackendExe = Join-Path $BinDir "laxcode.exe"
$InstanceFile = Join-Path ([Environment]::GetFolderPath("UserProfile")) ".laxcode\sse-code.instance"
$FrontendUrl = "http://127.0.0.1:5173"
$BackendProcess = $null
$ViteProcess = $null
$LocationPushed = $false
$PreviousProxyTarget = [Environment]::GetEnvironmentVariable("LAXCODE_PROXY_TARGET", "Process")

function Test-ProcessRunning {
    param([System.Diagnostics.Process]$Process)

    if ($null -eq $Process) {
        return $false
    }
    try {
        $Process.Refresh()
        return -not $Process.HasExited
    }
    catch {
        return $false
    }
}

function Test-HttpEndpoint {
    param([string]$Url)

    try {
        $Response = Invoke-WebRequest `
            -Uri $Url `
            -UseBasicParsing `
            -TimeoutSec 1 `
            -ErrorAction Stop
        return $Response.StatusCode -ge 200 -and $Response.StatusCode -lt 400
    }
    catch {
        return $false
    }
}

function Wait-ForBackend {
    param(
        [System.Diagnostics.Process]$Process,
        [string]$StatePath
    )

    for ($Attempt = 0; $Attempt -lt 300; $Attempt++) {
        if (-not (Test-ProcessRunning $Process)) {
            throw "LaxCode backend exited before becoming ready"
        }

        if (Test-Path -LiteralPath $StatePath -PathType Leaf) {
            try {
                $State = (Get-Content -LiteralPath $StatePath -Raw -ErrorAction Stop).Trim()
                $Parts = $State -split "`t", 3
                if ($Parts.Count -eq 3 -and
                    $Parts[0] -eq "v1" -and
                    $Parts[1] -eq ([string]$Process.Id) -and
                    -not [string]::IsNullOrWhiteSpace($Parts[2])) {
                    $BackendUrl = $Parts[2]
                    if (Test-HttpEndpoint "$BackendUrl/healthz") {
                        return $BackendUrl
                    }
                }
            }
            catch {
                # The backend may be replacing a stale record. Retry until the
                # complete PID-matching line is readable.
            }
        }
        Start-Sleep -Milliseconds 100
    }
    throw "LaxCode backend did not become healthy within 30 seconds"
}

function Wait-ForFrontend {
    param(
        [System.Diagnostics.Process]$Backend,
        [System.Diagnostics.Process]$Vite,
        [string]$Url
    )

    for ($Attempt = 0; $Attempt -lt 300; $Attempt++) {
        if (-not (Test-ProcessRunning $Backend)) {
            throw "LaxCode backend exited while starting the web UI"
        }
        if (-not (Test-ProcessRunning $Vite)) {
            throw "Vite exited before becoming ready; port 5173 may already be in use"
        }
        if (Test-HttpEndpoint $Url) {
            return
        }
        Start-Sleep -Milliseconds 100
    }
    throw "Web UI did not become healthy within 30 seconds"
}

function Stop-ProcessTree {
    param([System.Diagnostics.Process]$Process)

    if (-not (Test-ProcessRunning $Process)) {
        return
    }
    & taskkill.exe /PID $Process.Id /T /F 2>$null | Out-Null
    try {
        $Process.WaitForExit(5000)
    }
    catch {
        # The process is already gone or cannot be waited on; cleanup continues.
    }
}

try {
    foreach ($CommandName in @("go", "node", "pnpm", "taskkill.exe")) {
        if ($null -eq (Get-Command $CommandName -ErrorAction SilentlyContinue)) {
            throw "required command not found: $CommandName"
        }
    }

    Push-Location $RepoRoot
    $LocationPushed = $true

    & pnpm --dir $WebDir install --frozen-lockfile
    if ($LASTEXITCODE -ne 0) {
        throw "pnpm install failed with exit code $LASTEXITCODE"
    }

    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    & go build -o $BackendExe .\cmd\main
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed with exit code $LASTEXITCODE"
    }

    $BackendProcess = Start-Process `
        -FilePath $BackendExe `
        -ArgumentList @("-sse", "-code", "-addr=127.0.0.1:0") `
        -WorkingDirectory $RepoRoot `
        -NoNewWindow `
        -PassThru

    $BackendUrl = Wait-ForBackend $BackendProcess $InstanceFile

    $env:LAXCODE_PROXY_TARGET = $BackendUrl
    $ViteProcess = Start-Process `
        -FilePath $env:ComSpec `
        -ArgumentList @("/d", "/s", "/c", "pnpm --dir web dev") `
        -WorkingDirectory $RepoRoot `
        -NoNewWindow `
        -PassThru

    Wait-ForFrontend $BackendProcess $ViteProcess $FrontendUrl

    Write-Host "LaxCode Web is ready: $FrontendUrl (backend: $BackendUrl)"
    Start-Process $FrontendUrl

    while ((Test-ProcessRunning $BackendProcess) -and (Test-ProcessRunning $ViteProcess)) {
        Start-Sleep -Seconds 1
    }

    if (-not (Test-ProcessRunning $BackendProcess)) {
        throw "LaxCode backend stopped"
    }
    throw "Vite web server stopped"
}
finally {
    if ($null -eq $PreviousProxyTarget) {
        Remove-Item Env:LAXCODE_PROXY_TARGET -ErrorAction SilentlyContinue
    }
    else {
        $env:LAXCODE_PROXY_TARGET = $PreviousProxyTarget
    }

    if ($null -ne $ViteProcess) {
        Stop-ProcessTree $ViteProcess
    }
    if (Test-ProcessRunning $BackendProcess) {
        Stop-Process -Id $BackendProcess.Id -Force -ErrorAction SilentlyContinue
        try {
            $BackendProcess.WaitForExit(5000)
        }
        catch {
            # The backend has already exited.
        }
    }

    if ($LocationPushed) {
        Pop-Location
    }
}
