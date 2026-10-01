package profile

import (
	_ "embed"
	"fmt"
	"slices"
	"sync"

	"github.com/BurntSushi/toml"
)

// Network groups are data only, like presets: named lists of hosts for
// profiles with network = "restricted". box's code never names a host.
//
//go:embed netgroups.toml
var netGroupsTOML string

// NetGroup is a named list of hosts.
type NetGroup struct {
	ID    string `toml:"id"`
	Label string `toml:"label"`
	// Kind is "docs" or "service" for groups a profile can list, and
	// "program" for a program's own servers.
	Kind     string   `toml:"kind"`
	Programs []string `toml:"programs"` // kind "program": the programs it serves
	Note     string   `toml:"note"`     // a caution shown with the group
	Hosts    []string `toml:"hosts"`
}

var netGroups = sync.OnceValue(func() []NetGroup {
	var file struct {
		Group []NetGroup `toml:"group"`
	}
	md, err := toml.Decode(netGroupsTOML, &file)
	if err == nil && len(md.Undecoded()) > 0 {
		err = fmt.Errorf("unknown key %s", md.Undecoded()[0])
	}
	if err != nil {
		panic("box: netgroups.toml: " + err.Error()) // embedded data; the tests parse it
	}
	return file.Group
})

// NetGroups returns the groups a profile can list under allow.groups, in
// the order the editor shows them.
func NetGroups() []NetGroup {
	return slices.DeleteFunc(slices.Clone(netGroups()), func(g NetGroup) bool { return g.Kind == "program" })
}

// OwnHosts returns the servers program itself needs: where it logs in and
// where its model runs. Every restricted profile of the program allows
// them, so restricting the network can't lock the program out. It returns
// nil for a program box has no list for.
func OwnHosts(program string) []string {
	var hosts []string
	for _, g := range netGroups() {
		if g.Kind == "program" && slices.Contains(g.Programs, program) {
			hosts = append(hosts, g.Hosts...)
		}
	}
	return hosts
}

// AllowSet is one named part of a restricted profile's allowlist. Custom
// is the profile's own hosts, which the user typed: they may lead into the
// LAN, where box's lists may not.
type AllowSet struct {
	Name   string
	Hosts  []string
	Custom bool
}

// Allowed lists everything a restricted profile may reach: the program's
// own servers, then its groups, then its custom hosts and addresses.
func (p Profile) Allowed() []AllowSet {
	var sets []AllowSet
	if own := OwnHosts(p.Program); len(own) > 0 {
		sets = append(sets, AllowSet{Name: p.Program + "'s own servers", Hosts: own})
	}
	for _, g := range NetGroups() {
		if slices.Contains(p.Allow.Groups, g.ID) {
			sets = append(sets, AllowSet{Name: g.ID, Hosts: g.Hosts})
		}
	}
	if len(p.Allow.Hosts) > 0 {
		sets = append(sets, AllowSet{Name: "custom", Hosts: p.Allow.Hosts, Custom: true})
	}
	return sets
}
