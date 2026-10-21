package studio

import (
	"maps"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/kit"
)

// Merge folds a resolved kit chain into one StudioKit. chain[0] is the root and
// the last element is the kit itself; later links override earlier ones:
// egress unions (sorted, deduplicated), mcp-servers / build-args key-override,
// secrets union with the child's description winning, prompts join root-first,
// and notes merge by name in first-appearance order with a child's note
// replacing the earlier one in place.
//
// The result's Base is the last kit's own Base with Kit cleared. It is for
// display only: builds walk the chain, not the merged Base. Inputs are not
// mutated (Base.ContextFiles is cloned shallowly).
func Merge(chain []StudioKit) StudioKit {
	if len(chain) == 0 {
		return StudioKit{}
	}
	out := chain[len(chain)-1]
	out.Base.Kit = ""
	out.Base.ContextFiles = maps.Clone(out.Base.ContextFiles)
	out.Egress = nil
	out.BuildArgs = nil
	out.Secrets = nil
	out.MCPServers = nil
	out.Notes = nil
	var prompts []string
	noteIdx := map[string]int{}
	for _, sk := range chain {
		out.Egress = append(out.Egress, sk.Egress...)
		for k, v := range sk.BuildArgs {
			if out.BuildArgs == nil {
				out.BuildArgs = map[string]string{}
			}
			out.BuildArgs[k] = v
		}
		for k, v := range sk.Secrets {
			if out.Secrets == nil {
				out.Secrets = map[string]kit.SecretConfig{}
			}
			out.Secrets[k] = v
		}
		for k, v := range sk.MCPServers {
			if out.MCPServers == nil {
				out.MCPServers = map[string]kit.MCPServer{}
			}
			out.MCPServers[k] = v
		}
		if sk.Prompt != "" {
			prompts = append(prompts, sk.Prompt)
		}
		for _, n := range sk.Notes {
			if i, ok := noteIdx[n.Name]; ok {
				out.Notes[i] = n
				continue
			}
			noteIdx[n.Name] = len(out.Notes)
			out.Notes = append(out.Notes, n)
		}
	}
	slices.Sort(out.Egress)
	out.Egress = slices.Compact(out.Egress)
	out.Prompt = strings.Join(prompts, "\n\n")
	return out
}
