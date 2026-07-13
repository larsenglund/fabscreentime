//go:build windows

package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

const taskName = "FabScreenTimeAgent"

// InstallDir is the per-user install location (%LOCALAPPDATA%\FabScreenTime).
func InstallDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, "FabScreenTime")
	}
	d, _ := os.UserConfigDir()
	return filepath.Join(d, "FabScreenTime")
}

// Install copies the agent into the per-user install dir and registers a hidden
// "at logon" Scheduled Task that runs it in the interactive session at LIMITED
// integrity, restarting on failure and never on a time limit (PLAN.md §4.9).
func Install(serverURL string) error {
	dir := InstallDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(dir, "agent.exe")
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(self), filepath.Clean(target)) {
		if err := copyFile(self, target); err != nil {
			return err
		}
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	xmlPath := filepath.Join(dir, "task.xml")
	if err := os.WriteFile(xmlPath, utf16LE(buildTaskXML(u.Username, target, "-server "+serverURL)), 0o600); err != nil {
		return err
	}
	defer os.Remove(xmlPath)

	if err := runSchtasks("/Create", "/TN", taskName, "/XML", xmlPath, "/F"); err != nil {
		return err
	}
	_ = runSchtasks("/Run", "/TN", taskName) // start now, no logout needed
	return nil
}

// Uninstall stops and removes the Scheduled Task. The installed exe and data dir
// are left in place (self-deleting a running exe is the rename-self problem);
// remove %LOCALAPPDATA%\FabScreenTime manually to fully clean up.
func Uninstall() error {
	_ = runSchtasks("/End", "/TN", taskName)
	return runSchtasks("/Delete", "/TN", taskName, "/F")
}

func runSchtasks(args ...string) error {
	cmd := exec.Command("schtasks.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks %v: %v: %s", args, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func buildTaskXML(userID, command, arguments string) string {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>FabScreenTime screentime logging agent</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + esc(userID) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(userID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>false</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(command) + `</Command>
      <Arguments>` + esc(arguments) + `</Arguments>
    </Exec>
  </Actions>
</Task>`
}

// utf16LE encodes s as UTF-16LE with a BOM — the encoding schtasks expects for a
// manifest declared as UTF-16.
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2+2)
	b = append(b, 0xFF, 0xFE)
	for _, r := range u {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
