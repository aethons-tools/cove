package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// resolveContract finds .at-work/<name>.json or .at-work/<name>.yml under dir.
// It returns os.ErrNotExist if neither exists, and an error if BOTH exist.
func resolveContract(dir, name string) (path, ext string, err error) {
	base := filepath.Join(dir, workSubdir, name)
	jsonPath, ymlPath := base+".json", base+".yml"
	_, jErr := os.Stat(jsonPath)
	_, yErr := os.Stat(ymlPath)
	switch {
	case jErr == nil && yErr == nil:
		return "", "", fmt.Errorf("%s: both %s.json and %s.yml exist; keep one", workSubdir, name, name)
	case jErr == nil:
		return jsonPath, ".json", nil
	case yErr == nil:
		return ymlPath, ".yml", nil
	default:
		return "", "", os.ErrNotExist
	}
}

// decodeFile reads path (JSON or YAML — JSON is valid YAML) into v. When strict,
// an unknown field is an error.
func decodeFile(path string, strict bool, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(strict)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

// encodeFile writes v to path, mirroring the extension: JSON (2-space) for .json,
// YAML for anything else (.yml/.yaml).
func encodeFile(path string, v any) error {
	var out []byte
	var err error
	if filepath.Ext(path) == ".json" {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err = enc.Encode(v); err != nil {
			return err
		}
		out = buf.Bytes()
	} else if out, err = yaml.Marshal(v); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
