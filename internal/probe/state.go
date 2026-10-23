package probe

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/rhisijmuiden/unify-syslog-server/internal/probeproto"
)

// State is what the probe remembers between restarts: its enrollment and
// the settings the main server last sent.
type State struct {
	Server   string              `json:"server"` // https://host:8080
	Pin      string              `json:"pin"`
	Key      string              `json:"key"`
	ID       int64               `json:"id"`
	Name     string              `json:"name"`
	Site     string              `json:"site"`
	Settings probeproto.Settings `json:"settings"`
	// Revoked is set when the server no longer accepts the key.
	Revoked bool `json:"revoked,omitempty"`
	// LastUpdate is reported to the server until the next update.
	LastUpdate *probeproto.UpdateReport `json:"last_update,omitempty"`
}

func (s State) Enrolled() bool { return s.Key != "" && s.Server != "" }

func statePath(dir string) string { return filepath.Join(dir, "state.json") }

func loadState(dir string) (*State, error) {
	st := &State{Settings: probeproto.DefaultSettings()}
	b, err := os.ReadFile(statePath(dir))
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("%s: %w", statePath(dir), err)
	}
	st.Settings = st.Settings.Normalize()
	return st, nil
}

// save writes the state atomically. When run as root (the CLI) the file is
// given to the owner of the data directory, so the service can read it.
func (s *State) save(dir string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeOwned(dir, statePath(dir), b)
}

func writeOwned(dir, path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if fi, err := os.Stat(dir); err == nil && os.Geteuid() == 0 {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			os.Chown(tmp, int(st.Uid), int(st.Gid))
		}
	}
	return os.Rename(tmp, path)
}

// seqFile numbers buffered records: "<epoch> <next>". A new epoch starts
// when the file is missing (a new or wiped probe).
type seqFile struct {
	path  string
	mu    sync.Mutex
	epoch string
	next  uint64
}

func openSeq(dir string) (*seqFile, error) {
	s := &seqFile{path: filepath.Join(dir, "seq"), next: 1}
	if b, err := os.ReadFile(s.path); err == nil {
		f := strings.Fields(string(b))
		if len(f) == 2 {
			if n, err := strconv.ParseUint(f[1], 10, 64); err == nil && f[0] != "" {
				s.epoch, s.next = f[0], n
				return s, nil
			}
		}
	}
	e := make([]byte, 8)
	rand.Read(e)
	s.epoch = hex.EncodeToString(e)
	return s, s.save()
}

// take reserves n sequence numbers and returns the first.
func (s *seqFile) take(n int) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := s.next
	s.next += uint64(n)
	return first, s.save()
}

func (s *seqFile) save() error {
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s.epoch+" "+strconv.FormatUint(s.next, 10)+"\n"), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
