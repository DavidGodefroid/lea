package agents

import (
	"github.com/DavidGodefroid/lea/internal/providers"
	"github.com/DavidGodefroid/lea/internal/store"
)

// registry is the providers an agent may be started on.
func (m *Manager) registry() (*providers.Registry, error) {
	if m.cfg.Providers == nil {
		return providers.Builtin(), nil
	}
	return m.cfg.Providers()
}

// launchArgs is what follows `lea-agent run <name>` for an agent on prov: the CLI to run (lea-agent's
// --cli, left out for Claude Code), then the CLI's own arguments: the provider's flags, the model,
// the user's arguments, then the session to resume or else the first prompt (providers.Argv).
func launchArgs(prov providers.Provider, a store.Agent, session string, user []string, prompt string) ([]string, error) {
	argv, err := prov.Argv(a.Name, a.Workdir, a.Model, session, user, prompt)
	if err != nil {
		return nil, err
	}
	if err := checkArgs(argv); err != nil {
		return nil, err
	}
	head := []string{"--"}
	if prov.CLI != providers.Claude {
		head = []string{"--cli", prov.CLI, "--"}
	}
	return append(head, argv...), nil
}
