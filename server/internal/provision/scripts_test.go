package provision

import (
	"os/exec"
	"strings"
	"testing"
)

// The remote scripts at least parse (bash -n).
func TestScriptsParse(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	for name, s := range map[string]string{
		"upgrade": staged(upgradeScript, "/var/tmp/vpn-stage-test"),
		"install": staged(installScript(443), "/var/tmp/vpn-stage-test"),
		"check":   checkScript,
	} {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s script: %v\n%s", name, err, out)
		}
	}
}
