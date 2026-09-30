package profile

import (
	"embed"
	"fmt"
)

// Presets are data only: they pre-tick folders and switches for programs
// named in box's goals. box's code never checks for a program by name.
//
//go:embed presets/*.toml
var presets embed.FS

// Preset returns the starting profile for program: its preset if one is
// embedded, otherwise the generic default.
func Preset(program string) (Profile, error) {
	data, err := presets.ReadFile("presets/" + program + ".toml")
	if err != nil {
		return Default(program), nil
	}
	p, err := Parse(data, program)
	if err != nil {
		return Profile{}, fmt.Errorf("preset %s: %w", program, err)
	}
	p.Program = program
	if err := p.Validate(program); err != nil {
		return Profile{}, fmt.Errorf("preset %s: %w", program, err)
	}
	return p, nil
}

// HasPreset reports whether program has an embedded preset.
func HasPreset(program string) bool {
	_, err := presets.ReadFile("presets/" + program + ".toml")
	return err == nil
}
