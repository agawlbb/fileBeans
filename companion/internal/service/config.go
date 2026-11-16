package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Root struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Mount       string `json:"mount,omitempty"`
	MountSource string `json:"mountSource,omitempty"`
}
type Config struct {
	Normalization      NormalizationConfig `json:"normalization,omitempty"`
	Listen             string              `json:"listen"`
	DSN                string              `json:"dsn"`
	Token              string              `json:"token"`
	State              string              `json:"state"`
	Roots              []Root              `json:"roots"`
	GoogleClientID     string              `json:"googleClientID"`
	GoogleClientSecret string              `json:"googleClientSecret"`
	GoogleRedirect     string              `json:"googleRedirect"`
}

func Load(path string) (Config, error) {
	var c Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	if e != nil {
		return c, e
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8095"
	}
	if len(c.Token) < 32 || c.DSN == "" || !filepath.IsAbs(c.State) {
		return c, errors.New("configure DSN, absolute state directory, and token of at least 32 characters")
	}
	names := map[string]bool{}
	for _, r := range c.Roots {
		if r.Name == "" || r.Name == "drive" || r.Name == "upload" || names[r.Name] || !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) == "/" {
			return c, errors.New("invalid or duplicate root")
		}
		if (r.Mount == "") != (r.MountSource == "") {
			return c, errors.New("mount and mountSource must be configured together")
		}
		if within(filepath.Clean(c.State), filepath.Clean(r.Path)) || within(filepath.Clean(r.Path), filepath.Clean(c.State)) {
			return c, errors.New("private state must be separate from all storage roots")
		}
		for _, other := range c.Roots {
			if other.Name != r.Name && (within(r.Path, other.Path) || within(other.Path, r.Path)) {
				return c, errors.New("storage roots must not overlap")
			}
		}
		names[r.Name] = true
	}
	if c.GoogleClientID != "" && (!strings.HasPrefix(c.GoogleRedirect, "https://") || c.GoogleClientSecret == "") {
		return c, errors.New("Google requires client secret and HTTPS redirect")
	}
	c.Normalization = c.Normalization.defaults()
	if c.Normalization.Enabled {
		path, err := clean(c.Normalization.Path)
		if err != nil || path == "." || protected(path) || !names[c.Normalization.Source] || c.Normalization.StableSeconds < 1 {
			return c, errors.New("invalid normalization configuration")
		}
		for _, root := range c.Roots {
			if root.Name == c.Normalization.Source && root.Mount != "" {
				return c, errors.New("normalization requires VPS-local storage")
			}
		}
		c.Normalization.Path = path
		if c.Normalization.DriveFolder != "" && !driveID(c.Normalization.DriveFolder) {
			return c, errors.New("invalid normalization Drive folder")
		}
	}
	return c, nil
}
func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimRight(root, "/")+"/")
}
