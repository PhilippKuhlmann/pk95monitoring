package proxmox

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// version is GET /version.
type version struct {
	Version string `json:"version"`
	Release string `json:"release"`
	RepoID  string `json:"repoid"`
}

// clusterEntry is an entry of GET /cluster/status (type "cluster" or "node").
type clusterEntry struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Nodes   int    `json:"nodes"`
	Quorate num    `json:"quorate"`
	Online  num    `json:"online"`
	IP      string `json:"ip"`
}

// resource is an entry of GET /cluster/resources (node, qemu, lxc, storage, …).
type resource struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Node       string `json:"node"`
	Status     string `json:"status"`
	Name       string `json:"name"`
	VMID       num    `json:"vmid"`
	Template   num    `json:"template"`
	CPU        num    `json:"cpu"`
	MaxCPU     num    `json:"maxcpu"`
	Mem        num    `json:"mem"`
	MaxMem     num    `json:"maxmem"`
	Disk       num    `json:"disk"`
	MaxDisk    num    `json:"maxdisk"`
	Uptime     num    `json:"uptime"`
	NetIn      num    `json:"netin"`
	NetOut     num    `json:"netout"`
	DiskRead   num    `json:"diskread"`
	DiskWrite  num    `json:"diskwrite"`
	Storage    string `json:"storage"`
	Shared     num    `json:"shared"`
	PluginType string `json:"plugintype"`
	Content    string `json:"content"` // storage: comma separated content types
}

func (r resource) vmid() string { return strconv.Itoa(int(r.VMID)) }

// nodeStatus is GET /nodes/{node}/status.
type nodeStatus struct {
	CPU     num   `json:"cpu"`
	LoadAvg []num `json:"loadavg"`
	Memory  struct {
		Total num `json:"total"`
		Used  num `json:"used"`
	} `json:"memory"`
	Swap struct {
		Total num `json:"total"`
		Used  num `json:"used"`
	} `json:"swap"`
	RootFS struct {
		Total num `json:"total"`
		Used  num `json:"used"`
	} `json:"rootfs"`
	Uptime  num `json:"uptime"`
	Wait    num `json:"wait"` // IO delay, fraction of CPU time
	CPUInfo struct {
		Model   string `json:"model"`
		CPUs    num    `json:"cpus"`
		Sockets num    `json:"sockets"`
		Cores   num    `json:"cores"`
	} `json:"cpuinfo"`
	PVEVersion string `json:"pveversion"`
	KVersion   string `json:"kversion"`
}

// zfsPool is an entry of GET /nodes/{node}/disks/zfs.
type zfsPool struct {
	Name   string `json:"name"`
	Health string `json:"health"`
	Size   num    `json:"size"`
	Alloc  num    `json:"alloc"`
	Free   num    `json:"free"`
	Frag   num    `json:"frag"`
}

// disk is an entry of GET /nodes/{node}/disks/list.
type disk struct {
	DevPath string `json:"devpath"`
	Model   string `json:"model"`
	Serial  string `json:"serial"`
	Size    num    `json:"size"`
	Type    string `json:"type"` // ssd, hdd, nvme, usb, unknown
	Health  string `json:"health"`
	// Wearout is the remaining life in percent as PVE reports it (100 = new), "N/A" for HDDs.
	Wearout optNum `json:"wearout"`
}

// aptPackage is an entry of GET /nodes/{node}/apt/update.
type aptPackage struct {
	Package string `json:"Package"`
	Origin  string `json:"Origin"`
	Version string `json:"Version"`
}

// task is an entry of GET /nodes/{node}/tasks.
type task struct {
	UPID      string `json:"upid"`
	Node      string `json:"node"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	Status    string `json:"status"`
	StartTime num    `json:"starttime"`
	EndTime   num    `json:"endtime"`
}

// taskLine is one line of a task log.
type taskLine struct {
	N int    `json:"n"`
	T string `json:"t"`
}

func nodePath(node string) string { return "/nodes/" + url.PathEscape(node) }

func (c *client) version(ctx context.Context) (version, error) {
	var v version
	err := c.get(ctx, "/version", nil, &v)
	return v, err
}

func (c *client) resources(ctx context.Context) ([]resource, error) {
	var out []resource
	err := c.get(ctx, "/cluster/resources", nil, &out)
	return out, err
}

func (c *client) clusterStatus(ctx context.Context) ([]clusterEntry, error) {
	var out []clusterEntry
	err := c.get(ctx, "/cluster/status", nil, &out)
	return out, err
}

func (c *client) nodeStatus(ctx context.Context, node string) (nodeStatus, error) {
	var out nodeStatus
	err := c.get(ctx, nodePath(node)+"/status", nil, &out)
	return out, err
}

func (c *client) zfsPools(ctx context.Context, node string) ([]zfsPool, error) {
	var out []zfsPool
	err := c.get(ctx, nodePath(node)+"/disks/zfs", nil, &out)
	return out, err
}

func (c *client) disks(ctx context.Context, node string) ([]disk, error) {
	var out []disk
	err := c.get(ctx, nodePath(node)+"/disks/list", nil, &out)
	return out, err
}

func (c *client) aptUpdates(ctx context.Context, node string) ([]aptPackage, error) {
	var out []aptPackage
	err := c.get(ctx, nodePath(node)+"/apt/update", nil, &out)
	return out, err
}

// backupTasks lists the vzdump tasks of a node started since the given time. The node's task
// archive is used instead of /cluster/tasks: the cluster-wide list only keeps the latest
// couple of tasks per node, so a nightly backup can drop out of it within hours.
func (c *client) backupTasks(ctx context.Context, node string, since int64) ([]task, error) {
	var out []task
	q := url.Values{"typefilter": {"vzdump"}, "since": {strconv.FormatInt(since, 10)}, "limit": {"1000"}}
	err := c.get(ctx, nodePath(node)+"/tasks", q, &out)
	for i := range out {
		if out[i].Node == "" {
			out[i].Node = node
		}
	}
	return out, err
}

func (c *client) taskLog(ctx context.Context, node, upid string) ([]taskLine, error) {
	var out []taskLine
	err := c.get(ctx, nodePath(node)+"/tasks/"+url.PathEscape(upid)+"/log", url.Values{"limit": {"50000"}}, &out)
	return out, err
}

// onboot reads "Start at boot" of a guest (kind qemu or lxc).
func (c *client) onboot(ctx context.Context, node, kind, vmid string) (bool, error) {
	var cfg struct {
		OnBoot num `json:"onboot"`
	}
	err := c.get(ctx, nodePath(node)+"/"+kind+"/"+url.PathEscape(vmid)+"/config", nil, &cfg)
	return cfg.OnBoot == 1, err
}

var (
	failedBackupRe   = regexp.MustCompile(`Backup of VM (\d+) failed`)
	finishedBackupRe = regexp.MustCompile(`Finished Backup of VM (\d+)\b`)
)

// taskInfo is what the log of a vzdump task tells.
type taskInfo struct {
	job      string   // vzdump command line of the job ("" = unknown); identifies the job
	failed   []string // VMIDs whose backup failed
	finished []string // VMIDs backed up successfully
}

// parseTaskLog reads the job command line and the failed and finished guests of a vzdump log.
func parseTaskLog(lines []taskLine) taskInfo {
	var ti taskInfo
	seen := map[string]bool{}
	add := func(list *[]string, prefix, id string) {
		if !seen[prefix+id] {
			seen[prefix+id] = true
			*list = append(*list, id)
		}
	}
	for _, l := range lines {
		if ti.job == "" {
			if _, cmd, ok := strings.Cut(l.T, "starting new backup job: "); ok {
				ti.job = strings.TrimSpace(cmd)
			}
		}
		if m := failedBackupRe.FindStringSubmatch(l.T); m != nil && strings.Contains(l.T, "ERROR") {
			add(&ti.failed, "f", m[1])
		}
		if m := finishedBackupRe.FindStringSubmatch(l.T); m != nil {
			add(&ti.finished, "o", m[1])
		}
	}
	return ti
}

// failedGuests extracts the VMIDs of failed guests from a vzdump task log.
func failedGuests(lines []taskLine) []string { return parseTaskLog(lines).failed }
