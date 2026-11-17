package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	ListenAddr          string `json:"listen_addr"`
	Domain              string `json:"domain"`
	VBoxManage          string `json:"vbox_manage"`
	HostOnlyAdapter     string `json:"host_only_adapter"`
	BaseDisk            string `json:"base_disk"`
	BaseNVRAM           string `json:"base_nvram"`
	MemoryMB            int    `json:"memory_mb"`
	CPUs                int    `json:"cpus"`
	IPStart             string `json:"ip_start"`
	IPEnd               string `json:"ip_end"`
	DNSAddress          string `json:"dns_address"`
	DNSHost             string `json:"dns_host"`
	DNSVMName           string `json:"dns_vm_name"`
	DNSZoneFile         string `json:"dns_zone_file"`
	GuestUser           string `json:"guest_user"`
	GuestPassword       string `json:"guest_password"`
	DNSGuestUser        string `json:"dns_guest_user"`
	DNSGuestPassword    string `json:"dns_guest_password"`
	GuestNATConnection  string `json:"guest_nat_connection"`
	GuestDHCPConnection string `json:"guest_dhcp_connection"`
	GuestHostOnlyIF     string `json:"guest_hostonly_interface"`
	BootTimeoutSeconds  int    `json:"boot_timeout_seconds"`
	GuestTimeoutSeconds int    `json:"guest_timeout_seconds"`
	StartType           string `json:"start_type"`
	MaxUploadMB         int64  `json:"max_upload_mb"`
}

func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("leer %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("config.json inválido: %w", err)
	}
	if v := os.Getenv("HTTPAAS_GUEST_PASSWORD"); v != "" {
		c.GuestPassword = v
	}
	if v := os.Getenv("HTTPAAS_DNS_PASSWORD"); v != "" {
		c.DNSGuestPassword = v
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	if c.MemoryMB == 0 {
		c.MemoryMB = 1024
	}
	if c.CPUs == 0 {
		c.CPUs = 1
	}
	if c.BootTimeoutSeconds == 0 {
		c.BootTimeoutSeconds = 150
	}
	if c.GuestTimeoutSeconds == 0 {
		c.GuestTimeoutSeconds = 120
	}
	if c.StartType == "" {
		c.StartType = "headless"
	}
	if c.MaxUploadMB == 0 {
		c.MaxUploadMB = 25
	}
	if c.GuestNATConnection == "" {
		c.GuestNATConnection = "NAT"
	}
	if c.GuestDHCPConnection == "" {
		c.GuestDHCPConnection = "Wired connection 1"
	}
	if c.GuestHostOnlyIF == "" {
		c.GuestHostOnlyIF = "enp0s8"
	}
	c.Domain = strings.TrimSuffix(c.Domain, ".")
	return c, c.Validate()
}

func (c Config) Validate() error {
	required := map[string]string{
		"domain":            c.Domain,
		"vbox_manage":       c.VBoxManage,
		"host_only_adapter": c.HostOnlyAdapter,
		"base_disk":         c.BaseDisk,
		"base_nvram":        c.BaseNVRAM,
		"ip_start":          c.IPStart,
		"ip_end":            c.IPEnd,
		"dns_address":       c.DNSAddress,
		"dns_host":          c.DNSHost,
		"dns_vm_name":       c.DNSVMName,
		"dns_zone_file":     c.DNSZoneFile,
		"guest_user":        c.GuestUser,
		"dns_guest_user":    c.DNSGuestUser,
	}
	for k, v := range required {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("falta %q en config.json", k)
		}
	}
	return nil
}

func (c Config) FQDN(name string) string { return name + "." + c.Domain }
func (c Config) URL(name string) string  { return "http://" + c.FQDN(name) }
