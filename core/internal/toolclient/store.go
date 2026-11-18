package toolclient

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"unicode/utf8"
)

const maxProfileBytes = 16384
const browserAttemptFilename = "browser-attempt.json"

// storeFS is the small native boundary for private storage. The validation,
// record bytes, state transitions, and recovery rules remain shared across OSes.
type storeFS interface {
	close() error
	checkDir() error
	openRead(name string) (*os.File, error)
	createExclusive(name string) (*os.File, error)
	syncFile(file *os.File) error
	syncDir() error
	rename(oldName, newName string, replace bool) error
	remove(name string) error
}

// Store holds an exclusive process lock until Close, including during login.
// Keep it open across load, network mutation and durable replacement.
type Store struct {
	fs storeFS
}

func (s *Store) Close() error {
	if !s.valid() {
		return nil
	}
	e := s.fs.close()
	s.fs = nil
	return e
}

func (s *Store) valid() bool {
	return s != nil && s.fs != nil
}

func (s *Store) syncDir() error {
	if !s.valid() {
		return ErrProfile
	}
	return s.fs.syncDir()
}

func (s *Store) openProfile() (*os.File, error) {
	return s.openPrivateFile("profile.json")
}

func (s *Store) openPrivateFile(name string) (*os.File, error) {
	if !s.valid() {
		return nil, ErrProfile
	}
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return nil, ErrProfile
	}
	return s.fs.openRead(name)
}
func (s *Store) Load() (*Profile, error) {
	p, _, err := s.LoadProfileSnapshot()
	return p, err
}

// LoadProfileSnapshot returns the strict active profile and the exact bytes
// observed through the same no-follow descriptor. Callers use those bytes for
// compare-before-activate after network work performed without holding Store.
func (s *Store) LoadProfileSnapshot() (*Profile, []byte, error) {
	f, e := s.openProfile()
	if e != nil {
		return nil, nil, e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, maxProfileBytes+1))
	if e != nil || len(raw) > maxProfileBytes || !utf8.Valid(raw) || !validEscapedUnicode(raw) {
		return nil, nil, ErrProfile
	}
	p, err := decodeProfile(raw)
	if err != nil {
		return nil, nil, err
	}
	return p, raw, nil
}

func decodeProfile(raw []byte) (*Profile, error) {
	// JSON decoder otherwise silently accepts duplicate keys. Token walk rejects
	// duplicates at all object depths before typed strict-schema decoding.
	d := json.NewDecoder(bytes.NewReader(raw))
	if uniqueJSON(d) != nil {
		return nil, ErrProfile
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrProfile
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(raw, &shape) != nil {
		return nil, ErrProfile
	}
	var header struct {
		Version int    `json:"version"`
		Method  string `json:"method"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return nil, ErrProfile
	}
	switch header.Version {
	case 1:
		if header.Method != "" || !exactKeys(shape, []string{"version", "config", "accountId", "requestId", "secret", "state"}, []string{"connectionId", "createdAt", "expiresAt"}) {
			return nil, ErrProfile
		}
		var config map[string]json.RawMessage
		if json.Unmarshal(shape["config"], &config) != nil || !exactKeys(config, []string{"apiOrigin", "providerOrigin", "publishableKey", "label", "capabilities", "lifetimeSeconds"}, nil) {
			return nil, ErrProfile
		}
		var p Profile
		d = json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&p) != nil || p.Validate() != nil {
			return nil, ErrProfile
		}
		return &p, nil
	case browserProfileVersion:
		if header.Method != browserLoginMethod || !exactKeys(shape, []string{"version", "method", "config", "accountId", "requestId", "secret", "state", "connectionId", "createdAt", "expiresAt"}, nil) {
			return nil, ErrProfile
		}
		var config map[string]json.RawMessage
		if json.Unmarshal(shape["config"], &config) != nil || !exactKeys(config, []string{"apiOrigin", "browserOrigin", "label", "capabilities", "lifetimeSeconds"}, nil) {
			return nil, ErrProfile
		}
		var record profileV2JSON
		d = json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&record) != nil {
			return nil, ErrProfile
		}
		p := Profile{
			Version:       record.Version,
			Method:        record.Method,
			BrowserOrigin: record.Config.BrowserOrigin,
			Config: Config{
				APIOrigin:       record.Config.APIOrigin,
				Label:           record.Config.Label,
				Capabilities:    record.Config.Capabilities,
				LifetimeSeconds: record.Config.LifetimeSeconds,
			},
			AccountID: record.AccountID, RequestID: record.RequestID,
			Secret: record.Secret, State: record.State,
			ConnectionID: record.ConnectionID, CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt,
		}
		if p.Validate() != nil {
			return nil, ErrProfile
		}
		return &p, nil
	default:
		return nil, ErrProfile
	}
}
func uniqueJSON(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	if t == nil {
		return ErrProfile
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			key, ok := k.(string)
			if e != nil || !ok || seen[key] {
				return ErrProfile
			}
			seen[key] = true
			if e = uniqueJSON(d); e != nil {
				return e
			}
		}
		t, e = d.Token()
		if e != nil || t != json.Delim('}') {
			return ErrProfile
		}
	case '[':
		for d.More() {
			if e = uniqueJSON(d); e != nil {
				return e
			}
		}
		t, e = d.Token()
		if e != nil || t != json.Delim(']') {
			return ErrProfile
		}
	default:
		return ErrProfile
	}
	return nil
}
func (s *Store) Save(p *Profile) error {
	if !s.valid() || p == nil || p.Version != 1 || p.Method != "" {
		return ErrProfile
	}
	if p.Validate() != nil || s.fs.checkDir() != nil {
		return ErrProfile
	}
	if current, err := s.Load(); err == nil {
		if current.Method == browserLoginMethod {
			return ErrProfile
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Existing unsafe targets reject even though rename itself would not follow.
	f, e := s.openProfile()
	if e == nil {
		f.Close()
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	raw, e := json.Marshal(p)
	if e != nil || len(raw) > maxProfileBytes || !utf8.Valid(raw) || !validEscapedUnicode(raw) {
		return ErrProfile
	}
	raw = append(raw, '\n')
	var b [16]byte
	if _, e = rand.Read(b[:]); e != nil {
		return ErrProfile
	}
	name := ".pending-" + hex.EncodeToString(b[:])
	tmp, e := s.fs.createExclusive(name)
	if e != nil {
		return ErrProfile
	}
	defer func() { tmp.Close(); _ = s.fs.remove(name) }()
	if _, e = tmp.Write(raw); e != nil {
		return ErrProfile
	}
	if e = s.fs.syncFile(tmp); e != nil {
		return ErrProfile
	}
	if e = tmp.Close(); e != nil {
		return ErrProfile
	}
	if e = s.fs.rename(name, "profile.json", true); e != nil {
		return ErrProfile
	}
	if s.syncDir() != nil {
		return ErrProfile
	}
	return nil
}

func (s *Store) LoadPendingBrowserAttempt() (*PendingBrowserAttempt, error) {
	attempt, _, err := s.LoadPendingBrowserAttemptSnapshot()
	return attempt, err
}

func (s *Store) LoadPendingBrowserAttemptSnapshot() (*PendingBrowserAttempt, []byte, error) {
	f, err := s.openPrivateFile(browserAttemptFilename)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxProfileBytes+1))
	if err != nil || len(raw) > maxProfileBytes || !utf8.Valid(raw) || !validEscapedUnicode(raw) {
		return nil, nil, ErrProfile
	}
	attempt, err := decodePendingBrowserAttempt(raw)
	if err != nil {
		return nil, nil, err
	}
	return attempt, raw, nil
}

func decodePendingBrowserAttempt(raw []byte) (*PendingBrowserAttempt, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if uniqueJSON(d) != nil {
		return nil, ErrProfile
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrProfile
	}
	var shape map[string]json.RawMessage
	if json.Unmarshal(raw, &shape) != nil || !exactKeys(shape, []string{"version", "method", "state", "config", "requestId", "toolSecret", "pollProof"}, nil) {
		return nil, ErrProfile
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(shape["config"], &config) != nil || !exactKeys(config, []string{"apiOrigin", "browserOrigin", "label", "capabilities", "lifetimeSeconds"}, nil) {
		return nil, ErrProfile
	}
	var attempt PendingBrowserAttempt
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&attempt) != nil || attempt.Validate() != nil {
		return nil, ErrProfile
	}
	return &attempt, nil
}

func (s *Store) SavePendingBrowserAttempt(attempt *PendingBrowserAttempt) error {
	return s.savePendingBrowserAttempt(attempt, browserAttemptWriteHooks{})
}

func (s *Store) savePendingBrowserAttempt(attempt *PendingBrowserAttempt, hooks browserAttemptWriteHooks) error {
	if !s.valid() || attempt.Validate() != nil || s.fs.checkDir() != nil {
		return ErrProfile
	}
	raw, err := json.Marshal(attempt)
	if err != nil || len(raw) > maxProfileBytes || !utf8.Valid(raw) || !validEscapedUnicode(raw) {
		return ErrProfile
	}
	raw = append(raw, '\n')
	existing, err := s.LoadPendingBrowserAttempt()
	if err == nil {
		if !reflect.DeepEqual(existing, attempt) {
			return ErrBrowserAttemptExists
		}
		syncDir := hooks.syncDir
		if syncDir == nil {
			syncDir = s.syncDir
		}
		if syncDir() != nil {
			return ErrProfile
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	syncDir := hooks.syncDir
	if syncDir == nil {
		syncDir = s.syncDir
	}
	return s.writeOnceWithHooks(browserAttemptFilename, raw, hooks.beforeRename, syncDir)
}

type browserAttemptWriteHooks struct {
	beforeRename func() error
	syncDir      func() error
}

// Remove is only called after confirmed self-revocation (or explicit local clear).
func (s *Store) Remove() error {
	if !s.valid() {
		return ErrProfile
	}
	_, _, profileErr := s.LoadProfileSnapshot()
	profileExists := profileErr == nil
	if profileErr != nil && !errors.Is(profileErr, os.ErrNotExist) {
		return profileErr
	}
	_, pendingErr := s.LoadPendingBrowserAttempt()
	pendingExists := pendingErr == nil
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return pendingErr
	}
	if !profileExists && !pendingExists {
		return os.ErrNotExist
	}
	// Explicit local forget also clears a retained browser pairing so a later
	// login cannot resume an attempt the user deliberately discarded. Keep each
	// unlink durable before proceeding to the next record; a failed second step
	// leaves the active profile recoverable and the operation safe to retry.
	if pendingExists {
		if s.fs.remove(browserAttemptFilename) != nil || s.syncDir() != nil {
			return ErrProfile
		}
	}
	if profileExists {
		if s.fs.remove("profile.json") != nil || s.syncDir() != nil {
			return ErrProfile
		}
	}
	return nil
}

func exactKeys(m map[string]json.RawMessage, required, optional []string) bool {
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
		if _, ok := m[key]; !ok {
			return false
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range m {
		if !allowed[key] {
			return false
		}
	}
	return true
}

// encoding/json replaces lone escaped surrogates; a profile must reject them.
func validEscapedUnicode(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			next, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || next < 0xdc00 || next > 0xdfff {
				return false
			}
			i += 6
		} else if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
	}
	return true
}
