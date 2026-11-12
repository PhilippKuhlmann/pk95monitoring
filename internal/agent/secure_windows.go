package agent

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// configDirSDDL grants full access to SYSTEM, the Administrators and the owner only, without
// inheriting from the parent: below %ProgramData% every local user could otherwise read the
// token in agent.json (Go ignores Unix permission bits on Windows).
const configDirSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;OW)"

// secureDir restricts the config directory; files in it inherit the protected ACL.
func secureDir(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(configDirSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("Zugriffsrechte für %s: %w", dir, err)
	}
	return nil
}
