package jam

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ServeHostKeys are the dotted serve-config paths that belong to one host.
// This is the one table; the docs (serve.md) mirror it.
var ServeHostKeys = []string{"listen", "admin-listen", "ui-hosts", "ui-origins", "dev-identity", "tls",
	"admin-tls", "store-postgres", "state-dir", "credentials-file", "pool.store", "previews.tls",
	// The followed config repo can't live in the repo it names.
	"config"}

// ServeConfigDiff is the result of comparing a repo's portable serve config
// with the running serve's.
type ServeConfigDiff struct {
	State string   `json:"state"` // same | differs | absent | unknown
	Keys  []string `json:"keys,omitempty"`
}

// MergeServeConfigs deep-copies docs and merges them in order: maps merge by
// key, scalars and lists replace.
func MergeServeConfigs(docs ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, d := range docs {
		mergeInto(out, d)
	}
	return out
}

func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				mergeInto(dm, sm)
				continue
			}
		}
		dst[k] = deepCopyAny(v)
	}
}

func deepCopyAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, e := range t {
			m[k] = deepCopyAny(e)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, e := range t {
			s[i] = deepCopyAny(e)
		}
		return s
	}
	return v
}

func lookupPath(doc map[string]any, path string) bool {
	cur := doc
	parts := strings.Split(path, ".")
	for i, p := range parts {
		v, ok := cur[p]
		if !ok {
			return false
		}
		if i == len(parts)-1 {
			return true
		}
		if cur, ok = v.(map[string]any); !ok {
			return false
		}
	}
	return false
}

// ServeHostKeysIn lists the host keys set in doc, in ServeHostKeys order.
func ServeHostKeysIn(doc map[string]any) []string {
	var out []string
	for _, k := range ServeHostKeys {
		if lookupPath(doc, k) {
			out = append(out, k)
		}
	}
	return out
}

// HostKeyRefusals returns one refusal per host key set in doc.
func HostKeyRefusals(file string, doc map[string]any) []string {
	var out []string
	for _, k := range ServeHostKeysIn(doc) {
		out = append(out, fmt.Sprintf("%s: %s is a host key; put it in jam.local.yml", file, k))
	}
	return out
}

// PortableServeConfig deep-copies doc minus the host keys. A map emptied by
// the removal is dropped.
func PortableServeConfig(doc map[string]any) map[string]any {
	out := deepCopyAny(doc).(map[string]any)
	for _, k := range ServeHostKeys {
		deletePath(out, strings.Split(k, "."))
	}
	return out
}

func deletePath(m map[string]any, parts []string) {
	if len(parts) == 1 {
		delete(m, parts[0])
		return
	}
	sub, ok := m[parts[0]].(map[string]any)
	if !ok {
		return
	}
	deletePath(sub, parts[1:])
	if len(sub) == 0 {
		delete(m, parts[0])
	}
}

// DiffServeConfig returns the sorted dotted leaf paths at which repo and
// running differ. A map present on one side only contributes each of its leaf
// paths; nil means no difference.
func DiffServeConfig(repo, running map[string]any) []string {
	var out []string
	diffInto("", repo, running, &out)
	sort.Strings(out)
	return out
}

func diffInto(prefix string, a, b map[string]any, out *[]string) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		path := prefix + k
		av, aok := a[k]
		bv, bok := b[k]
		am, aIsMap := av.(map[string]any)
		bm, bIsMap := bv.(map[string]any)
		switch {
		case aok && bok && aIsMap && bIsMap:
			diffInto(path+".", am, bm, out)
		case aok && !bok && aIsMap:
			diffInto(path+".", am, nil, out)
		case !aok && bok && bIsMap:
			diffInto(path+".", nil, bm, out)
		case aok && bok:
			if !reflect.DeepEqual(av, bv) {
				*out = append(*out, path)
			}
		default:
			*out = append(*out, path)
		}
	}
}
