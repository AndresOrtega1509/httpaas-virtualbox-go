package provision

import (
	"archive/zip"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"httpaas/internal/config"
	"httpaas/internal/model"
	"httpaas/internal/vbox"
)

type Provisioner struct {
	Cfg  config.Config
	VBox vbox.VBox
}

func New(cfg config.Config) *Provisioner {
	return &Provisioner{Cfg: cfg, VBox: vbox.VBox{Bin: cfg.VBoxManage, HostOnlyAdapter: cfg.HostOnlyAdapter, BaseDisk: cfg.BaseDisk, BaseNVRAM: cfg.BaseNVRAM, MemoryMB: cfg.MemoryMB, CPUs: cfg.CPUs, StartType: cfg.StartType}}
}

func (p *Provisioner) Ready() []string {
	var problems []string
	for label, path := range map[string]string{"VBoxManage": p.Cfg.VBoxManage, "disco Multiattach": p.Cfg.BaseDisk, "NVRAM base": p.Cfg.BaseNVRAM} {
		if _, err := os.Stat(path); err != nil {
			problems = append(problems, fmt.Sprintf("%s no encontrado: %s", label, path))
		}
	}
	if p.Cfg.GuestPassword == "" {
		problems = append(problems, "falta HTTPAAS_GUEST_PASSWORD (contraseña del usuario de las VMs Apache)")
	}
	if p.Cfg.DNSGuestPassword == "" {
		problems = append(problems, "falta HTTPAAS_DNS_PASSWORD (contraseña del usuario de ns)")
	}
	return problems
}

func ValidateHostname(s string) error {
	if len(s) < 2 || len(s) > 32 {
		return fmt.Errorf("el hostname debe tener entre 2 y 32 caracteres")
	}
	ok, _ := regexp.MatchString(`^[a-z][a-z0-9-]*[a-z0-9]$`, s)
	if !ok {
		return fmt.Errorf("use minúsculas, números y guiones; debe iniciar con letra y no terminar en guion")
	}
	if s == "ns" || s == "apache-base" {
		return fmt.Errorf("%q está reservado", s)
	}
	return nil
}

func ValidateZIP(path, deploymentType, executable string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("ZIP inválido: %w", err)
	}
	defer r.Close()
	foundIndex := false
	foundExec := false
	for _, f := range r.File {
		clean := filepath.ToSlash(filepath.Clean(f.Name))
		if strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return fmt.Errorf("el ZIP contiene rutas inseguras")
		}
		if strings.EqualFold(clean, "index.html") {
			foundIndex = true
		}
		if deploymentType == "executable" && clean == executable && !f.FileInfo().IsDir() {
			foundExec = true
		}
	}
	switch deploymentType {
	case "static":
		if !foundIndex {
			return fmt.Errorf("para un sitio estático, el ZIP debe contener index.html en la raíz")
		}
	case "executable":
		if !foundExec {
			return fmt.Errorf("el ZIP debe contener el ejecutable %q en la raíz", executable)
		}
	default:
		return fmt.Errorf("tipo de despliegue no válido")
	}
	return nil
}

func ValidateExecutableSettings(executable string, port int, args string) error {
	if ok, _ := regexp.MatchString(`^[A-Za-z0-9._-]+$`, executable); !ok {
		return fmt.Errorf("el nombre del ejecutable solo puede contener letras, números, punto, guion y guion bajo")
	}
	if port < 1024 || port > 65535 {
		return fmt.Errorf("el puerto interno debe estar entre 1024 y 65535")
	}
	if len(args) > 300 || strings.ContainsAny(args, "\r\n\x00") {
		return fmt.Errorf("argumentos de ejecución inválidos")
	}
	for _, token := range strings.Fields(args) {
		if ok, _ := regexp.MatchString(`^[A-Za-z0-9_./:{}=+-]+$`, token); !ok {
			return fmt.Errorf("argumento no permitido: %q", token)
		}
	}
	return nil
}

func (p *Provisioner) Create(inst model.Instance, zipPath string) error {
	if r := p.Ready(); len(r) > 0 {
		return fmt.Errorf("entorno no preparado: %s", strings.Join(r, "; "))
	}
	if err := p.VBox.Create(inst.Name); err != nil {
		return fmt.Errorf("crear VM: %w", err)
	}
	if err := p.VBox.Start(inst.Name); err != nil {
		return fmt.Errorf("iniciar VM: %w", err)
	}
	if err := p.VBox.WaitGuestAdditions(inst.Name, time.Duration(p.Cfg.BootTimeoutSeconds)*time.Second); err != nil {
		return err
	}
	if err := p.configureGuest(inst); err != nil {
		return fmt.Errorf("configurar hostname/red: %w", err)
	}
	if err := p.VBox.WaitGuestIPv4(inst.Name, inst.IP, 45*time.Second); err != nil {
		return fmt.Errorf("verificar IP asignada: %w", err)
	}
	if err := p.deploy(inst, zipPath); err != nil {
		return fmt.Errorf("desplegar ZIP: %w", err)
	}
	if err := p.upsertDNS(inst.Name, inst.IP); err != nil {
		return fmt.Errorf("actualizar Bind9: %w", err)
	}
	return nil
}

func (p *Provisioner) configureGuest(inst model.Instance) error {
	script := fmt.Sprintf(`set -e
hostnamectl set-hostname %s
if grep -q '^127\.0\.1\.1' /etc/hosts; then
  sed -i 's/^127\.0\.1\.1.*/127.0.1.1    %s/' /etc/hosts
else
  printf '\n127.0.1.1    %s\n' >> /etc/hosts
fi
nmcli connection modify %q ipv4.ignore-auto-dns yes || true
nmcli connection modify %q connection.autoconnect no || true
nmcli connection delete HostOnly >/dev/null 2>&1 || true
nmcli connection add type ethernet ifname %s con-name HostOnly ipv4.method manual ipv4.addresses %s/24 ipv4.gateway '' ipv4.dns %s ipv4.never-default yes connection.autoconnect yes
nmcli connection up HostOnly
nmcli connection down %q >/dev/null 2>&1 || true
nmcli connection up %q >/dev/null
nmcli connection modify HostOnly ipv4.dns %s connection.autoconnect yes
sed -i 's/^hosts:.*/hosts:          files myhostname dns mdns4_minimal/' /etc/nsswitch.conf
`, inst.Name, inst.Name, inst.Name, p.Cfg.GuestNATConnection, p.Cfg.GuestDHCPConnection, p.Cfg.GuestHostOnlyIF, inst.IP, p.Cfg.DNSAddress, p.Cfg.GuestNATConnection, p.Cfg.GuestNATConnection, p.Cfg.DNSAddress)
	_, err := p.VBox.GuestRunSudoScript(inst.Name, p.Cfg.GuestUser, p.Cfg.GuestPassword, script)
	return err
}

func (p *Provisioner) deploy(inst model.Instance, zipPath string) error {
	remote := "/tmp/" + filepath.Base(zipPath)
	if err := p.VBox.GuestCopyTo(inst.Name, p.Cfg.GuestUser, p.Cfg.GuestPassword, zipPath, remote); err != nil {
		return err
	}
	if inst.DeploymentType == "executable" {
		return p.deployExecutable(inst, remote)
	}
	return p.deployStatic(inst, remote)
}

func (p *Provisioner) deployStatic(inst model.Instance, remote string) error {
	script := fmt.Sprintf(`set -e
# Limpiar configuraciones de despliegues anteriores que pudiera traer la plantilla.
rm -f /etc/apache2/sites-enabled/httpaas-*.conf /etc/apache2/sites-available/httpaas-*.conf || true
for svc in /etc/systemd/system/httpaas-*.service; do
  [ -e "$svc" ] || continue
  name=$(basename "$svc" .service)
  systemctl disable --now "$name.service" >/dev/null 2>&1 || true
  rm -f "$svc"
done
systemctl daemon-reload

rm -rf /var/www/html/*
unzip -oq %q -d /var/www/html
test -f /var/www/html/index.html
chown -R www-data:www-data /var/www/html
chmod -R u=rwX,go=rX /var/www/html

# Cada VM publica un solo servicio. Reescribimos el VirtualHost por defecto desde cero.
cat > /etc/apache2/sites-available/000-default.conf <<'EOF'
<VirtualHost *:80>
    ServerName %s.%s
    DocumentRoot /var/www/html

    <Directory /var/www/html>
        Options -Indexes +FollowSymLinks
        AllowOverride None
        Require all granted
    </Directory>

    ErrorLog ${APACHE_LOG_DIR}/error.log
    CustomLog ${APACHE_LOG_DIR}/access.log combined
</VirtualHost>
EOF

a2ensite 000-default.conf >/dev/null 2>&1 || true
apache2ctl configtest
systemctl enable apache2 >/dev/null 2>&1 || true
systemctl restart apache2
rm -f %q
`, remote, inst.Name, p.Cfg.Domain, remote)
	_, err := p.VBox.GuestRunSudoScript(inst.Name, p.Cfg.GuestUser, p.Cfg.GuestPassword, script)
	return err
}

func (p *Provisioner) deployExecutable(inst model.Instance, remote string) error {
	appDir := "/opt/httpaas/" + inst.Name
	service := "httpaas-" + inst.Name
	args := strings.ReplaceAll(inst.AppArgs, "{port}", strconv.Itoa(inst.AppPort))
	args = strings.ReplaceAll(args, "{appdir}", appDir)
	argTokens := strings.Fields(args)
	quotedArgs := make([]string, 0, len(argTokens))
	for _, a := range argTokens {
		quotedArgs = append(quotedArgs, systemdQuote(a))
	}
	execStart := systemdQuote(appDir + "/" + inst.Executable)
	if len(quotedArgs) > 0 {
		execStart += " " + strings.Join(quotedArgs, " ")
	}
	script := fmt.Sprintf(`set -e
# Limpiar cualquier publicación Apache o servicio HTTPaaS heredado.
rm -f /etc/apache2/sites-enabled/httpaas-*.conf /etc/apache2/sites-available/httpaas-*.conf || true
for svc in /etc/systemd/system/httpaas-*.service; do
  [ -e "$svc" ] || continue
  name=$(basename "$svc" .service)
  systemctl disable --now "$name.service" >/dev/null 2>&1 || true
  rm -f "$svc"
done
systemctl daemon-reload

APPDIR=%q
mkdir -p "$APPDIR"
rm -rf "$APPDIR"/*
unzip -oq %q -d "$APPDIR"
test -f "$APPDIR/%s"
chmod +x "$APPDIR/%s"

cat > /etc/systemd/system/%s.service <<'EOF'
[Unit]
Description=HTTPaaS application %s
After=network.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable %s.service >/dev/null
systemctl restart %s.service
sleep 2
systemctl is-active --quiet %s.service

a2enmod proxy proxy_http >/dev/null
# Una VM = un servicio. El VirtualHost por defecto siempre se genera desde cero.
cat > /etc/apache2/sites-available/000-default.conf <<'EOF'
<VirtualHost *:80>
    ServerName %s.%s
    ProxyPreserveHost On
    ProxyPass / http://127.0.0.1:%d/
    ProxyPassReverse / http://127.0.0.1:%d/

    ErrorLog ${APACHE_LOG_DIR}/error.log
    CustomLog ${APACHE_LOG_DIR}/access.log combined
</VirtualHost>
EOF

a2ensite 000-default.conf >/dev/null 2>&1 || true
apache2ctl configtest
systemctl enable apache2 >/dev/null 2>&1 || true
systemctl restart apache2
rm -f %q
`, appDir, remote, inst.Executable, inst.Executable, service, inst.Name, appDir, execStart,
		service, service, service, inst.Name, p.Cfg.Domain, inst.AppPort, inst.AppPort, remote)
	_, err := p.VBox.GuestRunSudoScript(inst.Name, p.Cfg.GuestUser, p.Cfg.GuestPassword, script)
	return err
}

func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `%`, `%%`)
	return `"` + s + `"`
}

func (p *Provisioner) Delete(inst model.Instance) error {
	if err := p.deleteDNS(inst.Name); err != nil {
		return fmt.Errorf("eliminar DNS: %w", err)
	}
	if p.VBox.Exists(inst.Name) {
		if err := p.VBox.Delete(inst.Name); err != nil {
			return fmt.Errorf("eliminar VM: %w", err)
		}
	}
	return nil
}

func (p *Provisioner) upsertDNS(name, ip string) error { return p.rewriteDNS(name, ip, false) }
func (p *Provisioner) deleteDNS(name string) error     { return p.rewriteDNS(name, "", true) }

func (p *Provisioner) rewriteDNS(name, ip string, remove bool) error {
	vm := p.Cfg.DNSVMName
	if err := p.VBox.WaitGuestAdditions(vm, 30*time.Second); err != nil {
		return fmt.Errorf("ns/Guest Additions: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "httpaas-dns-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	local := filepath.Join(tmpDir, filepath.Base(p.Cfg.DNSZoneFile))
	if err := p.VBox.GuestCopyFrom(vm, p.Cfg.DNSGuestUser, p.Cfg.DNSGuestPassword, p.Cfg.DNSZoneFile, local); err != nil {
		return err
	}
	b, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	zone := string(b)
	lineRE := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s+IN\s+A\s+[0-9.]+\s*$\n?`)
	zone = lineRE.ReplaceAllString(zone, "")
	if !remove {
		zone = strings.TrimRight(zone, "\r\n") + fmt.Sprintf("\n%-16s IN      A       %s\n", name, ip)
	}
	serialRE := regexp.MustCompile(`(?is)(SOA\s+\S+\s+\S+\s*\(\s*)(\d+)`)
	m := serialRE.FindStringSubmatchIndex(zone)
	if m == nil {
		return fmt.Errorf("no se encontró serial SOA en %s", p.Cfg.DNSZoneFile)
	}
	oldSerial, _ := strconv.ParseUint(zone[m[4]:m[5]], 10, 64)
	zone = zone[:m[4]] + strconv.FormatUint(oldSerial+1, 10) + zone[m[5]:]
	if err := os.WriteFile(local, []byte(zone), 0644); err != nil {
		return err
	}
	remote := "/tmp/" + filepath.Base(local)
	if err := p.VBox.GuestCopyTo(vm, p.Cfg.DNSGuestUser, p.Cfg.DNSGuestPassword, local, remote); err != nil {
		return err
	}
	script := fmt.Sprintf(`set -e
cp %q %q.httpaas.bak
install -m 0644 %q %q
if named-checkzone %q %q; then
  systemctl reload bind9
  rm -f %q.httpaas.bak %q
else
  cp %q.httpaas.bak %q
  rm -f %q
  exit 1
fi
`, p.Cfg.DNSZoneFile, p.Cfg.DNSZoneFile, remote, p.Cfg.DNSZoneFile, p.Cfg.Domain, p.Cfg.DNSZoneFile, p.Cfg.DNSZoneFile, remote, p.Cfg.DNSZoneFile, p.Cfg.DNSZoneFile, remote)
	_, err = p.VBox.GuestRunSudoScript(vm, p.Cfg.DNSGuestUser, p.Cfg.DNSGuestPassword, script)
	return err
}

func (p *Provisioner) ReservedIPs() (map[string]bool, error) {
	used := map[string]bool{}

	// 1) Registros A existentes en Bind9. Esto evita reutilizar IPs cuando se
	// ejecuta una copia nueva de HTTPaaS cuyo instances.json empieza vacío.
	tmpDir, err := os.MkdirTemp("", "httpaas-usedips-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	localZone := filepath.Join(tmpDir, filepath.Base(p.Cfg.DNSZoneFile))
	if err := p.VBox.GuestCopyFrom(p.Cfg.DNSVMName, p.Cfg.DNSGuestUser, p.Cfg.DNSGuestPassword, p.Cfg.DNSZoneFile, localZone); err != nil {
		return nil, fmt.Errorf("leer zona DNS para reservar IPs: %w", err)
	}
	zoneBytes, err := os.ReadFile(localZone)
	if err != nil {
		return nil, err
	}
	aRE := regexp.MustCompile(`(?m)^\s*[A-Za-z0-9._-]+\s+IN\s+A\s+([0-9]+(?:\.[0-9]+){3})\s*$`)
	for _, m := range aRE.FindAllStringSubmatch(string(zoneBytes), -1) {
		if len(m) > 1 {
			used[m[1]] = true
		}
	}

	// 2) IPs publicadas por Guest Additions de cualquier VM registrada.
	// Así también se evitan colisiones con VMs de pruebas anteriores aunque
	// su registro DNS haya sido borrado accidentalmente.
	names, err := p.VBox.ListVMNames()
	if err == nil {
		for _, name := range names {
			ips, ipErr := p.VBox.GuestIPv4s(name)
			if ipErr != nil {
				continue
			}
			for _, ip := range ips {
				used[ip] = true
			}
		}
	}

	return used, nil
}

func AllocateIP(start, end string, used map[string]bool) (string, error) {
	a := ipToUint32(net.ParseIP(start))
	b := ipToUint32(net.ParseIP(end))
	if a == 0 || b == 0 || a > b {
		return "", fmt.Errorf("rango IP inválido: %s - %s", start, end)
	}
	for n := a; n <= b; n++ {
		ip := uint32ToIP(n).String()
		if !used[ip] {
			return ip, nil
		}
		if n == ^uint32(0) {
			break
		}
	}
	return "", fmt.Errorf("no quedan IP disponibles en %s - %s", start, end)
}
func ipToUint32(ip net.IP) uint32 {
	v := ip.To4()
	if v == nil {
		return 0
	}
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}
func uint32ToIP(v uint32) net.IP { return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)) }
