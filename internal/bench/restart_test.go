package bench

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestBenchStartCmd_NoShell(t *testing.T) {
	root := "/tmp/bench root; touch /tmp/pwned"
	cmd := benchStartCmd(root)
	if base := filepath.Base(cmd.Path); base != "bench" {
		t.Errorf("cmd.Path base = %q, want \"bench\" (must not go through a shell)", base)
	}
	if want := []string{"bench", "start"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("cmd.Args = %q, want %q", cmd.Args, want)
	}
	if cmd.Dir != root {
		t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, root)
	}
	if cmd.SysProcAttr == nil {
		t.Error("SysProcAttr should request a new session so the server survives")
	}
}

func TestBenchStartLogPath(t *testing.T) {
	got := benchStartLogPath("/opt/bench")
	if want := filepath.Join("/opt/bench", "logs", "bench-start.log"); got != want {
		t.Errorf("log path = %q, want %q", got, want)
	}
}
