package web

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// sidecarSuffix is appended to a labels file path to name its link sidecar.
const sidecarSuffix = ".quet-web.yaml"

// Link remembers which remote and project a labels file is published to. It holds no secrets.
type Link struct {
	Remote  string `yaml:"remote,omitempty"`
	Project string `yaml:"project"`
}

// SidecarPath returns the path of the link sidecar of the labels file at labelsPath.
func SidecarPath(labelsPath string) string { return labelsPath + sidecarSuffix }

// LoadLink reads the sidecar of labelsPath. A missing sidecar is not an error: ok is false.
func LoadLink(labelsPath string) (l Link, ok bool, err error) {
	path := SidecarPath(labelsPath)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Link{}, false, nil
	}
	if err != nil {
		return Link{}, false, fmt.Errorf("read link: %w", err)
	}
	if err := yaml.Unmarshal(data, &l); err != nil {
		return Link{}, false, fmt.Errorf("link %s: %w", path, err)
	}
	if l.Project == "" {
		return Link{}, false, fmt.Errorf("link %s: project is empty", path)
	}
	return l, true, nil
}

// SaveLink atomically writes the sidecar of labelsPath with mode 0644.
func SaveLink(labelsPath string, l Link) error {
	if l.Project == "" {
		return errors.New("link: project is empty")
	}
	data, err := yaml.Marshal(l)
	if err != nil {
		return fmt.Errorf("encode link: %w", err)
	}
	return writeFileAtomic(SidecarPath(labelsPath), data, 0o644)
}
