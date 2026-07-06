package config

import (
	"fmt"
	"sort"
)

// Task is the per-dispatch context at-dispatch hands a class's command.
type Task struct {
	Issue      string // e.g. "AET-42"
	Class      string // e.g. "implement"
	Repo       string // e.g. "aethons-tools/cove"
	Timeout    string // the class timeout, e.g. "30m"
	BriefPath  string // absolute path to the markdown brief
	ResultPath string // absolute path the command must write result.json to
}

// ResolveSecrets runs each secret's resolver via the injected resolve func and
// returns name→value. Values are held in memory only. The resolver is injected so
// tests never spawn processes.
func ResolveSecrets(secrets []Secret, resolve func([]string) (string, error)) (map[string]string, error) {
	out := make(map[string]string, len(secrets))
	for _, s := range secrets {
		v, err := resolve(s.Command)
		if err != nil {
			return nil, fmt.Errorf("resolve secret %s: %w", s.Name, err)
		}
		out[s.Name] = v
	}
	return out, nil
}

// BuildEnv returns the environment for a dispatch command: the fixed DISPATCH_*
// entries followed by resolved secrets (sorted by name for determinism).
func BuildEnv(t Task, secrets map[string]string) []string {
	env := []string{
		EnvIssue + "=" + t.Issue,
		EnvClass + "=" + t.Class,
		EnvRepo + "=" + t.Repo,
		EnvTimeout + "=" + t.Timeout,
		EnvBrief + "=" + t.BriefPath,
		EnvResult + "=" + t.ResultPath,
	}
	names := make([]string, 0, len(secrets))
	for n := range secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		env = append(env, n+"="+secrets[n])
	}
	return env
}
