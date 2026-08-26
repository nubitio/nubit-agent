package site

import "testing"

type fakeRunner struct { calls [][]string }
func (r *fakeRunner) Run(name string, args ...string) error { r.calls = append(r.calls, append([]string{name}, args...)); return nil }

func TestProvisionerCreatesIsolatedUserAndDocumentRoot(t *testing.T) {
	runner := &fakeRunner{}
	if err := (Provisioner{Runner: runner}).Create("example.com", "site-example"); err != nil { t.Fatal(err) }
	if len(runner.calls) != 2 { t.Fatalf("got %d commands", len(runner.calls)) }
	if runner.calls[0][0] != "useradd" || runner.calls[1][0] != "install" { t.Fatalf("unexpected commands: %#v", runner.calls) }
}
