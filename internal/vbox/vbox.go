package vbox

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type VBox struct {
	Bin             string
	HostOnlyAdapter string
	BaseDisk        string
	BaseNVRAM       string
	MemoryMB        int
	CPUs            int
	StartType       string
}

func (v VBox) run(args ...string) (string, error) {
	cmd := exec.Command(v.Bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("VBoxManage %s: %w: %s", strings.Join(redact(args), " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func redact(args []string) []string {
	out := append([]string(nil), args...)
	for i := range out {
		if strings.HasPrefix(out[i], "--password=") {
			out[i] = "--password=***"
		}
		if out[i] == "--password" && i+1 < len(out) {
			out[i+1] = "***"
		}
	}
	return out
}

func (v VBox) Exists(name string) bool {
	_, err := v.run("showvminfo", name, "--machinereadable")
	return err == nil
}

func (v VBox) Create(name string) error {
	if v.Exists(name) {
		return fmt.Errorf("ya existe una VM llamada %q", name)
	}
	if _, err := v.run("createvm", "--name", name, "--ostype", "Debian_64", "--register"); err != nil {
		return err
	}
	if _, err := v.run("modifyvm", name,
		"--memory", fmt.Sprint(v.MemoryMB),
		"--cpus", fmt.Sprint(v.CPUs),
		"--firmware", "efi",
		"--graphicscontroller", "vmsvga",
		"--nic1", "nat",
		"--nic2", "hostonly",
		"--hostonlyadapter2", v.HostOnlyAdapter,
		"--boot1", "disk",
		"--boot2", "none"); err != nil {
		return err
	}
	if _, err := v.run("storagectl", name, "--name", "SATA", "--add", "sata", "--controller", "IntelAhci"); err != nil {
		return err
	}
	if _, err := v.run("storageattach", name, "--storagectl", "SATA", "--port", "0", "--device", "0", "--type", "hdd", "--medium", v.BaseDisk); err != nil {
		return err
	}
	return v.copyNVRAM(name)
}

func (v VBox) copyNVRAM(name string) error {
	out, err := v.run("showvminfo", name, "--machinereadable")
	if err != nil {
		return err
	}
	cfg := parseMachineValue(out, "CfgFile")
	if cfg == "" {
		return fmt.Errorf("no se pudo determinar CfgFile de %s", name)
	}
	dst := filepath.Join(filepath.Dir(cfg), name+".nvram")
	b, err := os.ReadFile(v.BaseNVRAM)
	if err != nil {
		return fmt.Errorf("leer NVRAM base: %w", err)
	}
	if err := os.WriteFile(dst, b, 0644); err != nil {
		return fmt.Errorf("copiar NVRAM a %s: %w", dst, err)
	}
	return nil
}

func (v VBox) Start(name string) error {
	t := v.StartType
	if t == "" {
		t = "headless"
	}
	_, err := v.run("startvm", name, "--type", t)
	return err
}

func (v VBox) PowerOff(name string) {
	_, _ = v.run("controlvm", name, "poweroff")
	time.Sleep(1500 * time.Millisecond)
}

func (v VBox) Delete(name string) error {
	v.PowerOff(name)
	_, err := v.run("unregistervm", name, "--delete")
	return err
}

func (v VBox) ListVMNames() ([]string, error) {
	out, err := v.run("list", "vms")
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`(?m)^"([^"]+)"\s+\{[0-9a-fA-F-]+\}\s*$`)
	matches := re.FindAllStringSubmatch(out, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			names = append(names, m[1])
		}
	}
	return names, nil
}

func (v VBox) GuestIPv4s(name string) ([]string, error) {
	out, err := v.run("guestproperty", "enumerate", name)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`/VirtualBox/GuestInfo/Net/\d+/V4/IP, value: ([0-9]+(?:\.[0-9]+){3})`)
	matches := re.FindAllStringSubmatch(out, -1)
	seen := map[string]bool{}
	ips := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 && m[1] != "0.0.0.0" && !seen[m[1]] {
			seen[m[1]] = true
			ips = append(ips, m[1])
		}
	}
	return ips, nil
}

func normalizeMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	r := strings.NewReplacer(":", "", "-", "", ".", "")
	return r.Replace(s)
}

func (v VBox) HostOnlyMAC(name string) (string, error) {
	out, err := v.run("showvminfo", name, "--machinereadable")
	if err != nil {
		return "", err
	}
	mac := parseMachineValue(out, "macaddress2")
	if mac == "" {
		return "", fmt.Errorf("no se pudo determinar la MAC Host-Only de %s", name)
	}
	return normalizeMAC(mac), nil
}

// hostARPContains verifies that the expected IPv4 belongs to the VM's Host-Only
// NIC. This avoids depending exclusively on Guest Additions network properties,
// which are not always populated on some VirtualBox 7.0.x Linux guests.
func (v VBox) hostARPContains(name, expected string) (bool, string, error) {
	wantMAC, err := v.HostOnlyMAC(name)
	if err != nil {
		return false, "", err
	}
	// Trigger neighbour discovery. Failure is not fatal; arp may already have it.
	_ = exec.Command("ping", "-n", "1", "-w", "1000", expected).Run()
	out, _ := exec.Command("arp", "-a", expected).CombinedOutput()
	text := string(out)
	re := regexp.MustCompile(`(?im)^\s*` + regexp.QuoteMeta(expected) + `\s+([0-9a-f]{2}(?:-[0-9a-f]{2}){5})\s+`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return false, "", nil
	}
	gotMAC := normalizeMAC(m[1])
	return gotMAC == wantMAC, gotMAC, nil
}

func (v VBox) WaitGuestIPv4(name, expected string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last []string
	var lastARP string
	for time.Now().Before(deadline) {
		// First use the normal Guest Additions channel when available.
		ips, err := v.GuestIPv4s(name)
		if err == nil {
			last = ips
			for _, ip := range ips {
				if ip == expected {
					return nil
				}
			}
		}

		// Fallback: prove ownership from the Windows host using IP + MAC.
		ok, mac, arpErr := v.hostARPContains(name, expected)
		if arpErr == nil {
			lastARP = mac
			if ok {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	wantMAC, _ := v.HostOnlyMAC(name)
	return fmt.Errorf("no se pudo confirmar que %s pertenece a la VM %s; Guest Additions reportó [%s], MAC Host-Only esperada %s, MAC observada por ARP %s", expected, name, strings.Join(last, ", "), wantMAC, lastARP)
}

func (v VBox) WaitGuestAdditions(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := v.run("guestproperty", "get", name, "/VirtualBox/GuestAdd/Version")
		last = out
		if err == nil && !strings.Contains(strings.ToLower(out), "no value set") && strings.Contains(out, "Value:") {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("Guest Additions no respondió en %s; última salida: %s", name, strings.TrimSpace(last))
}

func (v VBox) GuestRun(name, user, password, exe string, args ...string) (string, error) {
	cmd := []string{"guestcontrol", name, "run", "--exe=" + exe, "--username=" + user, "--password=" + password, "--wait-stdout", "--wait-stderr", "--timeout=120000", "--"}
	cmd = append(cmd, args...)
	return v.run(cmd...)
}

// GuestStart launches a process in the guest and returns as soon as VirtualBox
// confirms that it started. This avoids keeping VBoxManage attached to the
// guest stdout/stderr for long scripts, which is more stable on VirtualBox 7.0.x
// Windows hosts.
func (v VBox) GuestStart(name, user, password, exe string, args ...string) error {
	cmd := []string{"guestcontrol", name, "start", "--exe=" + exe, "--username=" + user, "--password=" + password, "--timeout=30000", "--"}
	cmd = append(cmd, args...)
	_, err := v.run(cmd...)
	return err
}

func (v VBox) GuestCopyTo(name, user, password, localPath, guestPath string) error {
	abs, err := filepath.Abs(localPath)
	if err != nil {
		return err
	}
	// VirtualBox 7.0.x is more reliable with the positional destination syntax
	// (host-source guest-destination) than with --target-directory when the
	// destination directory already exists.
	_, err = v.run("guestcontrol", name, "copyto", "--username="+user, "--password="+password, abs, guestPath)
	return err
}

func (v VBox) GuestCopyFrom(name, user, password, guestPath, hostPath string) error {
	abs, err := filepath.Abs(hostPath)
	if err != nil {
		return err
	}
	_, err = v.run("guestcontrol", name, "copyfrom", "--username="+user, "--password="+password, guestPath, abs)
	return err
}

func (v VBox) GuestRunSudoScript(name, user, password, script string) (string, error) {
	tmp, err := os.CreateTemp("", "httpaas-script-*.sh")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	base := filepath.Base(path)
	defer os.Remove(path)
	if _, err := tmp.WriteString("#!/bin/bash\n" + script + "\n"); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	remote := "/tmp/" + base
	if err := v.GuestCopyTo(name, user, password, path, remote); err != nil {
		return "", err
	}

	logRemote := remote + ".log"
	rcRemote := remote + ".rc"
	lockRemote := remote + ".lock"

	// VirtualBox 7.0.22 on Windows can terminate VBoxManage.exe with
	// 0xc0000374 either immediately before or just after launching a Guest
	// Control process. To make this reliable we retry the launch. The flock
	// guard makes every retry idempotent: only one copy of the real script can
	// run at a time, and once rcRemote exists no retry executes it again.
	wrapper := fmt.Sprintf(
		"if [ ! -f %s ]; then ( flock -n 9 || exit 0; [ -f %s ] && exit 0; printf '%%s\\n' %s | sudo -S -p '' /bin/bash %s >%s 2>&1; printf '%%s\\n' $? >%s; rm -f %s ) 9>%s; fi",
		shellQuote(rcRemote), shellQuote(rcRemote), shellQuote(password), shellQuote(remote),
		shellQuote(logRemote), shellQuote(rcRemote), shellQuote(remote), shellQuote(lockRemote),
	)

	var lastStartErr error
	// Several short launch attempts are safer than trusting one VBoxManage
	// process. After each attempt, give the guest time to create the result
	// file. If the first attempt actually launched the job despite the Windows
	// crash, the lock prevents subsequent attempts from duplicating it.
	for attempt := 1; attempt <= 4; attempt++ {
		if v.guestFileExists(name, user, password, rcRemote) {
			break
		}
		if err := v.GuestStart(name, user, password, "/bin/bash", "-lc", wrapper); err != nil {
			lastStartErr = err
		}
		for i := 0; i < 8; i++ { // up to ~6 seconds per launch attempt
			if v.guestFileExists(name, user, password, rcRemote) {
				break
			}
			time.Sleep(750 * time.Millisecond)
		}
		if v.guestFileExists(name, user, password, rcRemote) {
			break
		}
	}

	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		if v.guestFileExists(name, user, password, rcRemote) {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !v.guestFileExists(name, user, password, rcRemote) {
		if lastStartErr != nil {
			return "", fmt.Errorf("el proceso remoto no reportó resultado después de varios reintentos; último error de VBoxManage: %w", lastStartErr)
		}
		return "", fmt.Errorf("el script remoto superó el tiempo de espera")
	}

	dir, err := os.MkdirTemp("", "httpaas-result-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	localRC := filepath.Join(dir, "result.rc")
	localLog := filepath.Join(dir, "result.log")
	if err := v.GuestCopyFrom(name, user, password, rcRemote, localRC); err != nil {
		return "", fmt.Errorf("leer resultado remoto: %w", err)
	}
	_ = v.GuestCopyFrom(name, user, password, logRemote, localLog)
	rcBytes, err := os.ReadFile(localRC)
	if err != nil {
		return "", err
	}
	logBytes, _ := os.ReadFile(localLog)
	output := strings.TrimSpace(string(logBytes))
	if strings.TrimSpace(string(rcBytes)) != "0" {
		return output, fmt.Errorf("script remoto terminó con código %s: %s", strings.TrimSpace(string(rcBytes)), output)
	}

	_, _ = v.run("guestcontrol", name, "rm", "--username="+user, "--password="+password, "--force", rcRemote, logRemote, lockRemote)
	return output, nil
}

func (v VBox) guestFileExists(name, user, password, guestPath string) bool {
	_, err := v.run("guestcontrol", name, "stat", "--username="+user, "--password="+password, guestPath)
	return err == nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func parseMachineValue(out, key string) string {
	s := bufio.NewScanner(strings.NewReader(out))
	prefix := key + "="
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, prefix) {
			return strings.Trim(strings.TrimPrefix(line, prefix), `"`)
		}
	}
	return ""
}
