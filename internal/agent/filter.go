package agent

import (
	"path"
	"slices"
	"strings"

	"github.com/shirou/gopsutil/v4/disk"

	"pk95monitoring/internal/model"
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

// matchName matches a service name against a pattern where "*" stands for any text
// ("veeam*", "*sql*"), case-insensitively (Windows service names are case-insensitive).
func matchName(pattern, name string) bool {
	return model.GlobMatch(strings.ToLower(pattern), strings.ToLower(name))
}

// isPattern reports whether a service entry is a pattern rather than one name.
func isPattern(s string) bool { return strings.Contains(s, "*") }

// svcEntry is one service of the host, for the selection.
type svcEntry struct {
	Name    string // service name (Windows) or unit name (Linux, "nginx.service")
	Display string // display name (Windows) or description (Linux)
	Active  bool   // running / active
}

// svcPick is a selected service with the check key it is reported under.
type svcPick struct {
	Key      string // "svc:<name>"
	Entry    svcEntry
	Found    bool // false: a required name the host does not have
	Optional bool // picked by a pattern or the central list
}

// bootGrace: services may still be starting during the first minutes after boot; a picked
// optional service that does not run yet is reported unknown meanwhile (no alert).
const bootGrace = 10 * 60

// svcBase is a unit name without ".service" (Windows names stay as they are).
func svcBase(name string) string { return strings.TrimSuffix(name, ".service") }

// svcMatches matches a pattern or name against a service: its name (also without ".service")
// or its display name.
func svcMatches(pattern string, e svcEntry) bool {
	return matchName(pattern, e.Name) || matchName(pattern, svcBase(e.Name)) || (e.Display != "" && matchName(pattern, e.Display))
}

// pickServices selects the services to report:
//   - required names (the device's list, no "*") must exist: a missing one is picked with
//     Found false; they are keyed by the configured spelling and never ignored;
//   - patterns of the device's list and every entry of the central list are optional: they
//     pick the matching services that run or start automatically (auto is asked only for
//     services that do not run), except ignored ones.
//
// Each service is picked once; the order is required first, then by name.
func pickServices(list []svcEntry, required, optional, ignore []string, auto func(svcEntry) bool) []svcPick {
	byName := make(map[string]svcEntry, len(list))
	for _, e := range list {
		byName[strings.ToLower(svcBase(e.Name))] = e
	}
	var out []svcPick
	seen := map[string]bool{}
	var patterns []string
	for _, r := range required {
		r = strings.TrimSpace(r)
		switch {
		case r == "":
		case isPattern(r):
			patterns = append(patterns, r)
		case !seen[strings.ToLower(svcBase(r))]:
			seen[strings.ToLower(svcBase(r))] = true
			e, ok := byName[strings.ToLower(svcBase(r))]
			if !ok {
				e = svcEntry{Name: r}
			}
			out = append(out, svcPick{Key: "svc:" + r, Entry: e, Found: ok})
		}
	}
	for _, o := range optional {
		if o = strings.TrimSpace(o); o != "" {
			patterns = append(patterns, o)
		}
	}
	if len(patterns) == 0 {
		return out
	}
	sorted := slices.Clone(list)
	slices.SortFunc(sorted, func(a, b svcEntry) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	for _, e := range sorted {
		k := strings.ToLower(svcBase(e.Name))
		if seen[k] || !slices.ContainsFunc(patterns, func(p string) bool { return svcMatches(p, e) }) {
			continue
		}
		if slices.ContainsFunc(ignore, func(p string) bool { p = strings.TrimSpace(p); return p != "" && svcMatches(p, e) }) {
			continue
		}
		if !e.Active && (auto == nil || !auto(e)) {
			continue
		}
		seen[k] = true
		out = append(out, svcPick{Key: "svc:" + svcBase(e.Name), Entry: e, Found: true, Optional: true})
	}
	return out
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
