$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

function Read-PlainPassword([string]$Prompt) {
    $secure = Read-Host $Prompt -AsSecureString
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
}

Write-Host "HTTPaaS - inicio seguro" -ForegroundColor Cyan
$env:HTTPAAS_GUEST_PASSWORD = Read-PlainPassword "Contraseña del usuario andres en la plantilla/VMs Apache"
$same = Read-Host "¿El servidor ns usa la misma contraseña? [S/n]"
if ([string]::IsNullOrWhiteSpace($same) -or $same.ToLower() -eq "s") {
    $env:HTTPAAS_DNS_PASSWORD = $env:HTTPAAS_GUEST_PASSWORD
} else {
    $env:HTTPAAS_DNS_PASSWORD = Read-PlainPassword "Contraseña del usuario andres en ns"
}

try {
    if (Test-Path ".\httpaas.exe") {
        & ".\httpaas.exe"
    } else {
        Write-Host "No se encontró httpaas.exe; ejecutando desde código fuente con Go..." -ForegroundColor Yellow
        go run .
    }
} finally {
    Remove-Item Env:HTTPAAS_GUEST_PASSWORD -ErrorAction SilentlyContinue
    Remove-Item Env:HTTPAAS_DNS_PASSWORD -ErrorAction SilentlyContinue
}
