package agent

import (
	"encoding/binary"
	"errors"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"

	"pk95monitoring/internal/model"
)

// serviceUserService marks per-user service templates and instances (CDPUserSvc_1a2b3 …),
// which come and go with logons.
const serviceUserService = 0x40

// bootGrace: automatic services may still be starting during the first minutes after boot.
const bootGrace = 10 * 60

type winService struct {
	Name, Display string
	Type, State   uint32
}

// serviceChecks reports the configured services (must run) and, with WatchAutoServices,
// every automatic-start service that is not running.
func serviceChecks(cfg Config, uptime uint64) []model.Check {
	if len(cfg.Services) == 0 && !cfg.WatchAutoServices {
		return nil
	}
	// Least privilege, so "pk95agent once" also works without elevation.
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT|windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return []model.Check{{Key: "services", Name: "Dienste", Group: groupServices, State: model.StateUnknown,
			Message: "Dienststeuerung nicht erreichbar: " + err.Error()}}
	}
	defer windows.CloseServiceHandle(h)
	list, err := enumServices(h)
	if err != nil {
		return []model.Check{{Key: "services", Name: "Dienste", Group: groupServices, State: model.StateUnknown,
			Message: "Dienste nicht lesbar: " + err.Error()}}
	}
	byName := make(map[string]winService, len(list))
	for _, s := range list {
		byName[strings.ToLower(s.Name)] = s
	}

	var out []model.Check
	configured := map[string]bool{}
	for _, name := range cfg.Services {
		name = strings.TrimSpace(name)
		if name == "" || configured[strings.ToLower(name)] {
			continue
		}
		configured[strings.ToLower(name)] = true
		s, ok := byName[strings.ToLower(name)]
		if !ok {
			out = append(out, model.Check{Key: "svc:" + name, Name: "Dienst " + name, Group: groupServices,
				State: model.StateCrit, Message: "Dienst nicht gefunden"})
			continue
		}
		// Key by the configured spelling, so it stays the same when the service disappears.
		ch := winServiceCheck(s)
		ch.Key = "svc:" + name
		out = append(out, ch)
	}

	if cfg.WatchAutoServices && uptime >= bootGrace {
		for _, s := range list {
			if s.State == windows.SERVICE_RUNNING || s.Type&serviceUserService != 0 ||
				configured[strings.ToLower(s.Name)] || ignored(s.Name, defaultIgnoredServices, cfg.IgnoreServices) {
				continue
			}
			if !mustRun(h, s.Name) {
				continue
			}
			out = append(out, winServiceCheck(s))
		}
	}
	return out
}

func winServiceCheck(s winService) model.Check {
	display := s.Display
	if display == "" {
		display = s.Name
	}
	ch := model.Check{Key: "svc:" + s.Name, Name: "Dienst " + display, Group: groupServices,
		Message: winStateText(s.State), Facts: map[string]string{"Dienstname": s.Name}}
	switch s.State {
	case windows.SERVICE_RUNNING:
		ch.State = model.StateOK
	case windows.SERVICE_START_PENDING, windows.SERVICE_CONTINUE_PENDING:
		ch.State = model.StateWarn
	default:
		ch.State = model.StateCrit
	}
	return ch
}

func winStateText(st uint32) string {
	switch st {
	case windows.SERVICE_RUNNING:
		return "läuft"
	case windows.SERVICE_STOPPED:
		return "beendet"
	case windows.SERVICE_START_PENDING:
		return "startet"
	case windows.SERVICE_STOP_PENDING:
		return "wird beendet"
	case windows.SERVICE_PAUSED, windows.SERVICE_PAUSE_PENDING:
		return "angehalten"
	case windows.SERVICE_CONTINUE_PENDING:
		return "wird fortgesetzt"
	}
	return "unbekannt"
}

// mustRun reports whether a service is set to start automatically and is not trigger-started
// (those stop again by design, e.g. when their device or event is gone).
func mustRun(scm windows.Handle, name string) bool {
	np, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	h, err := windows.OpenService(scm, np, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return false
	}
	s := &mgr.Service{Name: name, Handle: h}
	defer s.Close()
	c, err := s.Config()
	if err != nil || c.StartType != windows.SERVICE_AUTO_START {
		return false
	}
	return triggerCount(h) == 0
}

func triggerCount(h windows.Handle) uint32 {
	n := uint32(256)
	for range 4 {
		b := make([]byte, n)
		err := windows.QueryServiceConfig2(h, windows.SERVICE_CONFIG_TRIGGER_INFO, &b[0], n, &n)
		if err == nil {
			return binary.LittleEndian.Uint32(b[:4]) // SERVICE_TRIGGER_INFO.cTriggers
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
			return 0
		}
	}
	return 0
}

func enumServices(scm windows.Handle) ([]winService, error) {
	var needed, count uint32
	var buf []byte
	for {
		var p *byte
		if len(buf) > 0 {
			p = &buf[0]
		}
		err := windows.EnumServicesStatusEx(scm, windows.SC_ENUM_PROCESS_INFO, windows.SERVICE_WIN32,
			windows.SERVICE_STATE_ALL, p, uint32(len(buf)), &needed, &count, nil, nil)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_MORE_DATA) || needed <= uint32(len(buf)) {
			return nil, err
		}
		buf = make([]byte, needed)
	}
	if count == 0 {
		return nil, nil
	}
	entries := unsafe.Slice((*windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0])), int(count))
	out := make([]winService, 0, count)
	for _, e := range entries {
		out = append(out, winService{
			Name:    windows.UTF16PtrToString(e.ServiceName),
			Display: windows.UTF16PtrToString(e.DisplayName),
			Type:    e.ServiceStatusProcess.ServiceType,
			State:   e.ServiceStatusProcess.CurrentState,
		})
	}
	return out, nil
}
