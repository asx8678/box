package profile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Folders remembers which profile each program last used in each folder.
type Folders struct {
	path    string
	Entries map[string]map[string]string `toml:"folders"` // folder → program → profile
	// Damaged says why the file couldn't be read and where it was moved;
	// the memory then starts empty. Callers show it as a warning.
	Damaged string `toml:"-"`
}

// FoldersPath is where folder memory is stored.
func FoldersPath(d Dirs) string {
	return filepath.Join(d.Config, "folders.toml")
}

// LoadFolders reads folder memory; a missing file is empty memory. A file
// box can't read is moved aside to <path>.bad, so a damaged file costs only
// the memory, not every command; one owned or writable by someone else is
// still refused.
func LoadFolders(path string) (*Folders, error) {
	f := &Folders{path: path, Entries: map[string]map[string]string{}}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return f, nil
	}
	if err := checkOwner(path); err != nil {
		return nil, err
	}
	md, err := toml.DecodeFile(path, f)
	if err == nil && len(md.Undecoded()) > 0 {
		err = fmt.Errorf("unknown key %s", md.Undecoded()[0])
	}
	if err != nil {
		if rerr := os.Rename(path, path+".bad"); rerr != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		f.Entries = map[string]map[string]string{}
		f.Damaged = fmt.Sprintf("%s couldn't be read (%v); moved it to %s.bad and forgot which profile each folder uses", path, err, path)
	}
	if f.Entries == nil {
		f.Entries = map[string]map[string]string{}
	}
	return f, nil
}

// Get returns the profile program last used in folder.
func (f *Folders) Get(folder, program string) (string, bool) {
	name, ok := f.Entries[folder][program]
	return name, ok
}

// Set records that program used profile name in folder.
func (f *Folders) Set(folder, program, name string) {
	if f.Entries[folder] == nil {
		f.Entries[folder] = map[string]string{}
	}
	f.Entries[folder][program] = name
}

// Uses counts, per profile name, the folders where program uses it.
func (f *Folders) Uses(program string) map[string]int {
	used := map[string]int{}
	for _, progs := range f.Entries {
		if name, ok := progs[program]; ok {
			used[name]++
		}
	}
	return used
}

// Forget drops program from every folder.
func (f *Folders) Forget(program string) {
	for folder, progs := range f.Entries {
		delete(progs, program)
		if len(progs) == 0 {
			delete(f.Entries, folder)
		}
	}
}

// Save writes folder memory atomically.
func (f *Folders) Save() error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	return WriteAtomic(f.path, buf.Bytes())
}
