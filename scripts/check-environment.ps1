$ErrorActionPreference = "Continue"
Set-Location (Split-Path $PSScriptRoot -Parent)
$cfg = Get-Content .\config.json -Raw | ConvertFrom-Json

Write-Host "=== HTTPaaS · Diagnóstico ===" -ForegroundColor Cyan

function Check-Path($label, $path) {
    if (Test-Path $path) { Write-Host "[OK] $label -> $path" -ForegroundColor Green }
    else { Write-Host "[ERROR] $label no encontrado -> $path" -ForegroundColor Red }
}

Check-Path "VBoxManage" $cfg.vbox_manage
Check-Path "Disco Multiattach" $cfg.base_disk
Check-Path "NVRAM base" $cfg.base_nvram

if (Test-Path $cfg.vbox_manage) {
    Write-Host "`nVirtualBox:" -ForegroundColor Yellow
    & $cfg.vbox_manage --version
    Write-Host "`nTipo del disco base:" -ForegroundColor Yellow
    & $cfg.vbox_manage showmediuminfo $cfg.base_disk | Select-String -Pattern "Type|Tipo|Location|Ubicaci"
}

Write-Host "`nDNS y Guest Control en ns ($($cfg.dns_host)):" -ForegroundColor Yellow
Test-NetConnection $cfg.dns_host -Port 22 | Select-Object ComputerName,RemotePort,TcpTestSucceeded

Write-Host "`nConsulta DNS:" -ForegroundColor Yellow
nslookup ("ns." + $cfg.domain) $cfg.dns_address

Write-Host "`nEspacio libre C:" -ForegroundColor Yellow
Get-PSDrive C | Select-Object Name,@{N='FreeGB';E={[math]::Round($_.Free/1GB,2)}},@{N='UsedGB';E={[math]::Round($_.Used/1GB,2)}}

if (Test-Path $cfg.vbox_manage) {
    Write-Host "`nGuest Additions en ns (ns debe estar encendida):" -ForegroundColor Yellow
    & $cfg.vbox_manage guestproperty get $cfg.dns_vm_name "/VirtualBox/GuestAdd/Version"
}

if (Test-Path $cfg.vbox_manage) {
    Write-Host "`nGuest Additions heredadas de la plantilla (prueba con negocios si está encendida):" -ForegroundColor Yellow
    & $cfg.vbox_manage guestproperty get "negocios" "/VirtualBox/GuestAdd/Version"
}
