//go:build windows

package agent

import (
	"strings"
	"testing"
)

// The task manifest is the agent's only supervisor. A self-updated agent is an
// orphan the logon trigger cannot reach until the next reboot, so the definition
// must carry a repeating trigger — and a marker, or EnsureAutostart cannot tell
// an outdated registration from a current one.
func TestTaskXMLCarriesTheWatchdogTriggerAndMarker(t *testing.T) {
	xml := buildTaskXML(`DOMAIN\user`, `C:\ProgramData\FabScreenTime\agent.exe`, "-server https://example.test")
	for _, want := range []string{
		taskMarker,
		"<TimeTrigger>",
		"<Interval>PT15M</Interval>",
		"<LogonTrigger>",
		"<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task XML is missing %q", want)
		}
	}
}
