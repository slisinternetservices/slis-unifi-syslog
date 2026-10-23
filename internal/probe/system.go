package probe

import (
	"embed"
	"os"
	"path/filepath"
)

// The systemd unit and the files around it. Installers and the Raspberry Pi
// image build write them with "unify-syslog-probe install-files", so they
// exist in one place only. The service user and directories are created by
// systemd-sysusers and systemd-tmpfiles, which also works for an image
// prepared on another machine.

//go:embed system
var systemFiles embed.FS

var systemTargets = map[string]string{
	"system/unify-syslog-probe.service": "etc/systemd/system/unify-syslog-probe.service",
	"system/sysusers.conf":              "usr/lib/sysusers.d/unify-syslog-probe.conf",
	"system/tmpfiles.conf":              "usr/lib/tmpfiles.d/unify-syslog-probe.conf",
	"system/issue":                      "etc/issue.d/unify-syslog-probe.issue",
	"system/profile.sh":                 "etc/profile.d/unify-syslog-probe.sh",
}

// InstallFiles writes the system files below root ("/" for this machine)
// and enables the service.
func InstallFiles(root string) ([]string, error) {
	var out []string
	for src, dst := range systemTargets {
		b, err := systemFiles.ReadFile(src)
		if err != nil {
			return out, err
		}
		p := filepath.Join(root, dst)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return out, err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return out, err
		}
		out = append(out, p)
	}
	// systemctl enable, without needing systemd to run (image builds).
	wants := filepath.Join(root, "etc/systemd/system/multi-user.target.wants")
	if err := os.MkdirAll(wants, 0o755); err != nil {
		return out, err
	}
	link := filepath.Join(wants, "unify-syslog-probe.service")
	os.Remove(link)
	if err := os.Symlink("/etc/systemd/system/unify-syslog-probe.service", link); err != nil {
		return out, err
	}
	return append(out, link), nil
}
