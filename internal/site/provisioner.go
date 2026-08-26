package site

import (
	"fmt"
	"os/exec"
)

type Runner interface { Run(name string, args ...string) error }
type OSRunner struct{}
func (OSRunner) Run(name string, args ...string) error { return exec.Command(name, args...).Run() }

type Provisioner struct { Runner Runner }

func (p Provisioner) Create(domain, systemUser string) error {
	root := "/srv/nubit/sites/" + domain
	for _, command := range [][]string{
		{"useradd", "--system", "--create-home", "--shell", "/usr/sbin/nologin", systemUser},
		{"install", "-d", "-o", systemUser, "-g", systemUser, "-m", "0750", root + "/public"},
	} {
		if err := p.Runner.Run(command[0], command[1:]...); err != nil { return fmt.Errorf("%s: %w", command[0], err) }
	}
	return nil
}
