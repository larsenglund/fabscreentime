//go:build windows

package agent

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

const taskName = "FabScreenTimeAgent"

// taskMarker stamps the task definition with the template that produced it, so
// a newer agent can tell an outdated registration from a current one and repair
// it (EnsureAutostart). Bump it whenever buildTaskXML changes meaningfully.
const taskMarker = "task-v3"

// InstallDir is the install location: %ProgramData%\FabScreenTime.
//
// NOT %LOCALAPPDATA%: a hidden Scheduled Task launching an unsigned exe from the
// user's AppData\Local profile is the textbook malware-persistence pattern, and
// Windows' app-reputation heuristics silently block Task-Scheduler launches from
// there — the process is never created and no event is logged, while the same
// exe runs fine interactively and from ProgramData (verified 2026-07-24). A
// standard user can still create this folder without UAC, and the token stays
// DPAPI-encrypted per-user, so an all-users folder does not expose it.
func InstallDir() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return filepath.Join(d, "FabScreenTime")
	}
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
	if err := registerTask(dir, target, serverURL); err != nil {
		return err
	}
	_ = runSchtasks("/Run", "/TN", taskName) // start now, no logout needed
	return nil
}

// registerTask writes the task manifest and (re-)registers it, replacing any
// existing definition. Registering over a running task leaves that instance
// running; it only changes what the next start uses.
func registerTask(dir, target, serverURL string) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	xmlPath := filepath.Join(dir, "task.xml")
	if err := os.WriteFile(xmlPath, utf16LE(buildTaskXML(u.Username, target, "-server "+serverURL)), 0o600); err != nil {
		return err
	}
	defer os.Remove(xmlPath)
	return runSchtasks("/Create", "/TN", taskName, "/XML", xmlPath, "/F")
}

// EnsureAutostart re-registers the autostart task when it is missing or was
// written by an older agent. The task definition is only ever written by the
// installer, so without this a fix to it would never reach the installed fleet —
// and the fleet is exactly where it is needed: after a self-update the running
// agent is a child of a task instance that has already finished, so nothing
// supervises it any more. The refreshed definition adds a repeating trigger that
// starts the agent again if it is not running; the single-instance mutex keeps a
// firing that lands on a still-healthy agent from ever double-starting one.
//
// It is deliberately inert unless this process IS the installed copy: a dev run
// or a manual double-click from elsewhere must never touch the user's autostart.
func EnsureAutostart(serverURL string) {
	dir := InstallDir()
	target := filepath.Join(dir, "agent.exe")
	self, err := os.Executable()
	if err != nil || !strings.EqualFold(filepath.Clean(self), filepath.Clean(target)) {
		return
	}
	if taskIsCurrent() {
		return
	}
	log.Print("autostart task is missing or outdated; re-registering it")
	if err := registerTask(dir, target, serverURL); err != nil {
		log.Printf("autostart repair failed: %v", err)
		return
	}
	log.Print("autostart task re-registered")
}

// taskIsCurrent reports whether the registered task carries the current
// template's marker. schtasks emits UTF-16, so the NUL bytes are stripped before
// looking for the (ASCII) marker.
func taskIsCurrent() bool {
	cmd := exec.Command("schtasks.exe", "/Query", "/TN", taskName, "/XML", "ONE")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	out, err := cmd.Output()
	if err != nil {
		return false // not registered (or unreadable) — treat as needing repair
	}
	return bytes.Contains(bytes.ReplaceAll(out, []byte{0}, nil), []byte(taskMarker))
}

// Uninstall stops and removes the Scheduled Task, deletes the data files, and
// schedules deletion of the exe + install dir (PLAN.md §5.5). A running exe
// can't delete itself (the rename-self problem), so the exe and directory are
// removed by a short detached cmd that waits for handles to release.
func Uninstall() error {
	_ = runSchtasks("/End", "/TN", taskName)
	if err := runSchtasks("/Delete", "/TN", taskName, "/F"); err != nil {
		return err
	}
	dir := InstallDir()
	// Data files first (not write-locked), so they're gone even if the exe/dir
	// removal is delayed.
	for _, f := range []string{"credentials.json", "queue.json", "agent.log", "agent.log.1", "update-state.json", "enroll.json", "task.xml"} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	scheduleCleanup(dir)
	return nil
}

// scheduleCleanup spawns a detached cmd that waits briefly (for the stopped task
// and this process to release handles), then deletes the agent exe and the dir.
func scheduleCleanup(dir string) {
	script := fmt.Sprintf(
		`ping -n 3 127.0.0.1 >nul & del /f /q "%s\agent.exe" "%s\agent.exe.old" >nul 2>nul & rmdir /s /q "%s" >nul 2>nul`,
		dir, dir, dir)
	cmd := exec.Command("cmd.exe", "/c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	_ = cmd.Start()
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
    <Description>FabScreenTime screentime logging agent [` + taskMarker + `]</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + esc(userID) + `</UserId>
    </LogonTrigger>
    <TimeTrigger>
      <Repetition>
        <Interval>PT15M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
      <StartBoundary>2020-01-01T00:00:00</StartBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(userID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <!-- Parallel, not IgnoreNew: every repeating-trigger firing must actually
         launch a process so a dead agent is replaced within the interval. With
         IgnoreNew, Task Scheduler's own view of "an instance is already running"
         can wedge after a crash and silently stop restarting it (a device stayed
         dark for days that way). The agent's single-instance mutex — not this
         policy — is what prevents a real double-run: the probe this spawns while
         the agent is healthy loses the mutex and exits in seconds. -->
    <MultipleInstancesPolicy>Parallel</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
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
