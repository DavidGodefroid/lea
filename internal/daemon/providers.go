package daemon

import (
	"fmt"

	"github.com/DavidGodefroid/lea/internal/providers"
)

// SetProviders sets the registry of the providers agents may name (the user's providers file,
// read at each call); without it, the built-in ones.
func (d *Daemon) SetProviders(f func() (*providers.Registry, error)) {
	d.mu.Lock()
	d.providers = f
	d.mu.Unlock()
}

func (d *Daemon) registry() (*providers.Registry, error) {
	d.mu.Lock()
	f := d.providers
	d.mu.Unlock()
	if f == nil {
		return providers.Builtin(), nil
	}
	return f()
}

// checkProvider checks what an agent names: "" (the default) or a provider of the registry, "" or
// a model id, and "" or an advisor the provider takes (Claude Code only, #407). The errors never
// echo an overlong or unsafe value.
func (d *Daemon) checkProvider(provider, model, advisor string) error {
	if len(model) > providers.MaxModelLen {
		return fmt.Errorf("model is %d bytes, limit %d", len(model), providers.MaxModelLen)
	}
	if err := providers.CheckModel(model); err != nil {
		return err
	}
	if len(advisor) > providers.MaxModelLen {
		return fmt.Errorf("advisor is %d bytes, limit %d", len(advisor), providers.MaxModelLen)
	}
	if err := providers.CheckModel(advisor); err != nil {
		return fmt.Errorf("advisor: %w", err)
	}
	if provider == "" && advisor == "" {
		return nil
	}
	reg, err := d.registry()
	if err != nil {
		return err
	}
	name := provider
	if name == "" {
		d.mu.Lock()
		name = d.defProvider
		d.mu.Unlock()
	}
	if name == "" {
		name = providers.Default
	}
	p, err := reg.Get(name)
	if err != nil {
		return err
	}
	return p.CheckAdvisor(advisor)
}

// SetDefaultProvider records the configuration's default provider (LEA_PROVIDER) for providers_list.
func (d *Daemon) SetDefaultProvider(name string) {
	d.mu.Lock()
	d.defProvider = name
	d.mu.Unlock()
}

type providerView struct {
	Name    string `json:"name"`
	CLI     string `json:"cli"`
	Model   string `json:"model,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// providersList is what an agent may start agents on: names, CLIs and default models, never an
// endpoint's address or key file.
func (d *Daemon) providersList() (any, error) {
	reg, err := d.registry()
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	def := d.defProvider
	d.mu.Unlock()
	if def == "" {
		def = providers.Default
	}
	var out []providerView
	for _, n := range reg.Names() {
		p, _ := reg.Get(n)
		out = append(out, providerView{Name: p.Name, CLI: p.CLI, Model: p.Model, Default: p.Name == def})
	}
	return out, nil
}
