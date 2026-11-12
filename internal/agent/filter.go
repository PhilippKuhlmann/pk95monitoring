package agent

import (
	"path"
	"slices"
	"strings"

	"github.com/shirou/gopsutil/v4/disk"
)

// Pseudo and virtual file systems never worth monitoring.
var skipFSTypes = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "overlay": true, "squashfs": true, "proc": true, "sysfs": true,
	"cgroup": true, "cgroup2": true, "autofs": true, "nsfs": true, "tracefs": true, "debugfs": true,
	"fuse.lxcfs": true, "efivarfs": true, "securityfs": true, "pstore": true, "bpf": true,
	"configfs": true, "fusectl": true, "mqueue": true, "hugetlbfs": true, "devpts": true,
	"binfmt_misc": true, "ramfs": true, "rpc_pipefs": true, "devfs": true, "nullfs": true, "devicefs": true,
}

var skipMountPrefixes = []string{
	"/snap/", "/proc/", "/sys/", "/dev/", "/run/", "/var/lib/docker/", "/var/lib/containers/",
	"/var/snap/", "/System/Volumes/", "/private/var/", "/Library/Developer/",
}

// skipMount reports whether a mount is not monitored. Proxmox guest volumes (ZFS subvol-…)
// belong to the guest and are monitored over the Proxmox API instead.
func skipMount(fstype, mount string) bool {
	if skipFSTypes[strings.ToLower(fstype)] || strings.HasPrefix(fstype, "fuse.") && fstype != "fuse.sshfs" && fstype != "fuse.glusterfs" {
		return true
	}
	if mount == "" {
		return true
	}
	for _, p := range skipMountPrefixes {
		if strings.HasPrefix(mount, p) || mount+"/" == p {
			return true
		}
	}
	base := path.Base(mount)
	return strings.HasPrefix(base, "subvol-") || strings.HasPrefix(base, "basevol-")
}

// filterPartitions drops pseudo mounts and keeps one mount per device (the shortest path,
// e.g. "/" instead of a bind mount of the same file system).
func filterPartitions(parts []disk.PartitionStat) []disk.PartitionStat {
	byDev := map[string]int{}
	var out []disk.PartitionStat
	for _, p := range parts {
		if skipMount(p.Fstype, p.Mountpoint) || p.Mountpoint != "/" && readOnly(p.Opts) {
			continue // read-only mounts (ISO, recovery, images) are full by design
		}
		dev := p.Device
		if dev == "" || dev == "none" {
			dev = p.Mountpoint
		}
		if i, ok := byDev[dev]; ok {
			if len(p.Mountpoint) < len(out[i].Mountpoint) {
				out[i] = p
			}
			continue
		}
		byDev[dev] = len(out)
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b disk.PartitionStat) int { return strings.Compare(a.Mountpoint, b.Mountpoint) })
	return out
}

func readOnly(opts []string) bool {
	return slices.Contains(opts, "ro") || slices.Contains(opts, "read-only") || slices.Contains(opts, "rdonly")
}

// Interface name prefixes that are virtual or per-guest (Proxmox tap/fw*, Docker, libvirt …).
// Physical NICs (eth*, en*, wl*), bonds and Proxmox bridges (vmbr*) are kept.
var skipIfacePrefixes = []string{
	"veth", "docker", "br-", "virbr", "tap", "fwbr", "fwpr", "fwln", "tun", "cali", "cni",
	"flannel", "kube-", "vnet", "zt", "dummy", "sit", "ip6tnl", "gre", "teql", "ifb",
	// macOS
	"utun", "awdl", "llw", "anpi", "gif", "stf", "bridge", "ap1",
}

// Windows pseudo adapters (matched case-insensitively anywhere in the name).
var skipIfaceContains = []string{"loopback", "isatap", "teredo", "pseudo-interface", "6to4", "-wfp ", "-qos ", "npcap"}

// skipIface reports whether an interface is not monitored.
func skipIface(name string) bool {
	n := strings.ToLower(name)
	if n == "" || strings.Contains(n, "*") { // Windows "Local Area Connection* 2" (Wi-Fi Direct)
		return true
	}
	if rest, ok := strings.CutPrefix(n, "lo"); ok && strings.Trim(rest, "0123456789") == "" {
		return true // lo, lo0
	}
	if strings.HasPrefix(n, "vethernet") {
		return false // Hyper-V host NIC, e.g. "vEthernet (External)"
	}
	for _, p := range skipIfacePrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, s := range skipIfaceContains {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// matchName matches a service name against a pattern with an optional trailing "*",
// case-insensitively (Windows service names are case-insensitive).
func matchName(pattern, name string) bool {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return len(name) >= len(p) && strings.EqualFold(name[:len(p)], p)
	}
	return strings.EqualFold(pattern, name)
}

// ignored reports whether name matches one of the patterns.
func ignored(name string, patterns ...[]string) bool {
	for _, ps := range patterns {
		for _, p := range ps {
			if matchName(strings.TrimSpace(p), name) {
				return true
			}
		}
	}
	return false
}

// defaultIgnoredServices are automatic Windows services that stop by design (updaters,
// on-demand helpers) and would only cause noise with WatchAutoServices.
var defaultIgnoredServices = []string{
	"sppsvc", "gupdate", "gupdatem", "edgeupdate", "edgeupdatem", "MapsBroker", "RemoteRegistry",
	"WbioSrvc", "tiledatamodelsvc", "CDPSvc", "GoogleUpdaterService*", "GoogleUpdaterInternalService*",
	"clr_optimization_*", "TrustedInstaller", "BITS", "wuauserv", "UsoSvc", "DoSvc", "WaaSMedicSvc",
	"ShellHWDetection", "sedsvc", "RtkAudioUniversalService", "MicrosoftEdgeElevationService",
}
