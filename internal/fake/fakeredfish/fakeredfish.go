// Package fakeredfish simulates the Redfish API of a server management controller (shaped like
// a Dell iDRAC 9 in a PowerEdge R650) for tests and the demo. It must not import core.
package fakeredfish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"pk95monitoring/internal/model"
)

// Login of the simulated controller.
const (
	User     = "monitoring"
	Password = "nur-lesen"
)

// Server is the fake controller.
type Server struct {
	mu         sync.Mutex
	srv        *httptest.Server
	powerState string
	psu2Health string // health of power supply 2 ("Critical" = lost input)
	predicted  bool   // drive 4 predicts a failure
}

// New starts the controller: power supply 2 has lost its input power (redundancy lost) and
// one drive of the RAID 5 predicts a failure; everything else is fine.
func New() *Server {
	s := &Server{powerState: "On", psu2Health: "Critical", predicted: true}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	return s
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Host returns the address of the server.
func (s *Server) Host() string {
	h, _, _ := net.SplitHostPort(s.srv.Listener.Addr().String())
	return h
}

// Port returns the port of the server.
func (s *Server) Port() int {
	_, p, _ := net.SplitHostPort(s.srv.Listener.Addr().String())
	n, _ := strconv.Atoi(p)
	return n
}

// Fingerprint returns the SHA-256 fingerprint of the certificate.
func (s *Server) Fingerprint() string {
	sum := sha256.Sum256(s.srv.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

// Config returns a working configuration.
func (s *Server) Config() model.RedfishConfig {
	return model.RedfishConfig{Port: s.Port(), Username: User, Password: Password, Fingerprint: s.Fingerprint()}
}

// SetPSU2 sets the health of power supply 2 ("OK" restores it).
func (s *Server) SetPSU2(health string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.psu2Health = health
}

// SetPowerState sets the power state of the system ("On", "Off").
func (s *Server) SetPowerState(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.powerState = state
}

type obj = map[string]any

func ref(p string) obj { return obj{"@odata.id": p} }

func refs(ps ...string) []obj {
	out := make([]obj, 0, len(ps))
	for _, p := range ps {
		out = append(out, ref(p))
	}
	return out
}

func coll(ps ...string) obj { return obj{"Members": refs(ps...), "Members@odata.count": len(ps)} }

func st(health string) obj { return obj{"State": "Enabled", "Health": health} }

const (
	sysPath  = "/redfish/v1/Systems/System.Embedded.1"
	chasPath = "/redfish/v1/Chassis/System.Embedded.1"
	enclPath = "/redfish/v1/Chassis/Enclosure.Internal.0-1:RAID.Integrated.1-1"
	mgrPath  = "/redfish/v1/Managers/iDRAC.Embedded.1"
	raidPath = sysPath + "/Storage/RAID.Integrated.1-1"
	ahciPath = sysPath + "/Storage/AHCI.Embedded.1-1"
	gb       = 1e9
)

func driveID(i int) string {
	return "Disk.Bay." + strconv.Itoa(i) + ":Enclosure.Internal.0-1:RAID.Integrated.1-1"
}

func (s *Server) resources() map[string]any {
	wave := func(k float64) float64 { return math.Sin(float64(time.Now().Unix())/900 + k) }
	rollup := "OK"
	switch {
	case s.psu2Health != "OK":
		rollup = "Critical"
	case s.predicted:
		rollup = "Warning"
	}
	psuVolts, redundancy := 230.0, "OK"
	if s.psu2Health != "OK" {
		psuVolts, redundancy = 0, "Critical"
	}
	temp := func(name string, v, warn, crit float64) obj {
		return obj{"Name": name, "ReadingCelsius": math.Round(v), "UpperThresholdNonCritical": warn, "UpperThresholdCritical": crit, "Status": st("OK")}
	}
	fans := []obj{}
	for i := 1; i <= 6; i++ {
		fans = append(fans, obj{"Name": "System Board Fan" + strconv.Itoa(i) + "A", "Reading": math.Round(6800 + 400*wave(float64(i))),
			"ReadingUnits": "RPM", "Status": st("OK")})
	}
	drives := []string{}
	for i := range 5 {
		drives = append(drives, raidPath+"/Drives/"+driveID(i))
	}
	res := map[string]any{
		"/redfish/v1": obj{"RedfishVersion": "1.17.0", "Vendor": "Dell", "Product": "Integrated Dell Remote Access Controller",
			"Systems": ref("/redfish/v1/Systems"), "Chassis": ref("/redfish/v1/Chassis"), "Managers": ref("/redfish/v1/Managers")},
		"/redfish/v1/Systems":  coll(sysPath),
		"/redfish/v1/Chassis":  coll(chasPath, enclPath),
		"/redfish/v1/Managers": coll(mgrPath),
		sysPath: obj{"Id": "System.Embedded.1", "Manufacturer": "Dell Inc.", "Model": "PowerEdge R650", "SerialNumber": "CNIVC0012300AB",
			"SKU": "7XK2PQ3", "BiosVersion": "1.14.1", "HostName": "hv01", "PowerState": s.powerState,
			"Status":           obj{"State": "Enabled", "Health": rollup, "HealthRollup": rollup},
			"MemorySummary":    obj{"TotalSystemMemoryGiB": 256, "Status": obj{"Health": "OK", "HealthRollup": "OK", "State": "Enabled"}},
			"ProcessorSummary": obj{"Count": 2, "Model": "Intel(R) Xeon(R) Gold 5418Y", "Status": obj{"Health": "OK", "HealthRollup": "OK", "State": "Enabled"}},
			"Storage":          ref(sysPath + "/Storage")},
		chasPath: obj{"Id": "System.Embedded.1", "Name": "Computer System Chassis", "Thermal": ref(chasPath + "/Thermal"), "Power": ref(chasPath + "/Power")},
		enclPath: obj{"Id": "Enclosure.Internal.0-1:RAID.Integrated.1-1", "Name": "BP15G+ 0:1"},
		mgrPath:  obj{"Id": "iDRAC.Embedded.1", "Model": "15G Monolithic", "FirmwareVersion": "7.10.70.00"},
		chasPath + "/Thermal": obj{
			"Temperatures": []obj{
				temp("System Board Inlet Temp", 23+1.5*wave(1), 42, 47),
				temp("System Board Exhaust Temp", 38+3*wave(2), 70, 75),
				temp("CPU1 Temp", 58+6*wave(3), 93, 98),
				temp("CPU2 Temp", 61+6*wave(4), 93, 98),
			},
			"Fans":       fans,
			"Redundancy": []obj{{"Name": "System Board Fan Redundancy", "Mode": "N+m", "Status": st("OK")}},
		},
		chasPath + "/Power": obj{
			"PowerControl": []obj{{"PowerConsumedWatts": math.Round(318 + 25*wave(5))}},
			"PowerSupplies": []obj{
				{"Name": "PS1 Status", "Model": "PWR SPLY,1100W,RDNT,LTON", "PowerCapacityWatts": 1100, "LineInputVoltage": 230, "Status": st("OK")},
				{"Name": "PS2 Status", "Model": "PWR SPLY,1100W,RDNT,LTON", "PowerCapacityWatts": 1100, "LineInputVoltage": psuVolts, "Status": st(s.psu2Health)},
			},
			"Redundancy": []obj{{"Name": "System Board PS Redundancy", "Mode": "N+m", "Status": st(redundancy)}},
		},
		sysPath + "/Storage": coll(raidPath, ahciPath),
		ahciPath:             obj{"Id": "AHCI.Embedded.1-1", "Name": "C620 Series Chipset", "Status": st("OK"), "Drives": []obj{}},
		raidPath: obj{"Id": "RAID.Integrated.1-1", "Name": "PERC H755 Front", "Status": obj{"State": "Enabled", "Health": "OK", "HealthRollup": "Warning"},
			"StorageControllers": []obj{{"Name": "PERC H755 Front", "Model": "PERC H755 Front", "FirmwareVersion": "52.26.0-5179", "Status": st("OK")}},
			"Drives":             refs(drives...),
			"Volumes":            ref(raidPath + "/Volumes")},
		raidPath + "/Volumes": coll(raidPath+"/Volumes/Disk.Virtual.0:RAID.Integrated.1-1", raidPath+"/Volumes/Disk.Virtual.1:RAID.Integrated.1-1"),
		raidPath + "/Volumes/Disk.Virtual.0:RAID.Integrated.1-1": obj{"Id": "Disk.Virtual.0:RAID.Integrated.1-1", "Name": "OS", "RAIDType": "RAID1",
			"CapacityBytes": 479.5 * gb, "Status": st("OK"), "Operations": []obj{}},
		raidPath + "/Volumes/Disk.Virtual.1:RAID.Integrated.1-1": obj{"Id": "Disk.Virtual.1:RAID.Integrated.1-1", "Name": "VMs", "RAIDType": "RAID5",
			"CapacityBytes": 5760 * gb, "Status": st("OK"), "Operations": []obj{}},
	}
	for i := range 5 {
		d := obj{"Id": driveID(i), "Name": "SSD " + strconv.Itoa(i), "Model": "MZ7L3480HCHQAD3", "SerialNumber": "S6KMNC0T60" + strconv.Itoa(1000+i),
			"CapacityBytes": 480 * gb, "MediaType": "SSD", "Protocol": "SATA", "FailurePredicted": false, "PredictedMediaLifeLeftPercent": 97 - i, "Status": st("OK")}
		if i >= 2 {
			d["Model"], d["CapacityBytes"], d["SerialNumber"], d["Protocol"] = "MZILG1T9HCJRAD9", 1920*gb, "S5NHNA0W40"+strconv.Itoa(2000+i), "SAS"
		}
		if i == 4 && s.predicted {
			d["FailurePredicted"], d["Status"], d["PredictedMediaLifeLeftPercent"] = true, st("Warning"), 4
		}
		res[raidPath+"/Drives/"+driveID(i)] = d
	}
	return res
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json;odata.metadata=minimal")
	path := strings.TrimSuffix(r.URL.Path, "/")
	// the service root is readable without login, as on real controllers
	if u, p, ok := r.BasicAuth(); path != "/redfish/v1" && (!ok || u != User || p != Password) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(obj{"error": obj{"message": "Unable to complete the operation because an invalid username and/or password is entered"}})
		return
	}
	res, ok := s.resources()[path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(obj{"error": obj{"message": "Resource not found"}})
		return
	}
	if m, ok := res.(obj); ok {
		m["@odata.id"] = path
	}
	_ = json.NewEncoder(w).Encode(res)
}
