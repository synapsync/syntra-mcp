package toolclient

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

// Private records share the profile directory and operation-wide lock. Names
// are derived only from validated UUIDs; caller text is never a path component.
func (s *Store) readRecord(name string) ([]byte, error) {
	if !s.valid() || s.fs.checkDir() != nil {
		return nil, ErrProfile
	}
	f, err := s.openPrivateFile(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4*maxResponseBytes+1))
	if err != nil || len(raw) > 4*maxResponseBytes {
		return nil, ErrProfile
	}
	return raw, nil
}

var errRetainedConflict = errors.New("retained record differs; explicit recovery required")

func (s *Store) writeOnce(name string, raw []byte) error {
	return s.writeOnceWithSync(name, raw, s.syncDir)
}

func (s *Store) writeOnceWithSync(name string, raw []byte, syncDir func() error) error {
	return s.writeOnceWithHooks(name, raw, nil, syncDir)
}

// writeOnceWithHooks keeps creation semantics identical to retained records
// while exposing a per-call pre-rename failure point for browser-intent tests.
// The hook is never stored on Store and cannot affect unrelated operations.
func (s *Store) writeOnceWithHooks(name string, raw []byte, beforeRename func() error, syncDir func() error) error {
	if len(raw) > 4*maxResponseBytes || !s.valid() || s.fs.checkDir() != nil {
		return ErrProfile
	}
	if syncDir == nil {
		syncDir = s.syncDir
	}
	old, err := s.readRecord(name)
	if err == nil {
		if !bytes.Equal(old, raw) {
			return errRetainedConflict
		}
		if syncDir() != nil {
			return ErrProfile
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.writeAtomicPrivateFile(name, raw, false, nil, beforeRename, syncDir)
}

// writeAtomicPrivateFile provides one no-follow, durable same-directory writer
// for create-once records and compare-checked atomic replacements. The caller
// supplies the exact prior bytes for replacement under Store's exclusive lock.
func (s *Store) writeAtomicPrivateFile(name string, raw []byte, replace bool, expected []byte, beforeRename func() error, syncDir func() error) error {
	if len(raw) > 4*maxResponseBytes || !s.valid() || s.fs.checkDir() != nil {
		return ErrProfile
	}
	current, err := s.readRecord(name)
	if replace {
		if err != nil || !bytes.Equal(current, expected) {
			return ErrProfile
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrProfile
	}
	if syncDir == nil {
		syncDir = s.syncDir
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return ErrProfile
	}
	tmpname := ".record-" + hex.EncodeToString(nonce[:])
	f, err := s.fs.createExclusive(tmpname)
	if err != nil {
		return ErrProfile
	}
	defer func() { f.Close(); _ = s.fs.remove(tmpname) }()
	if _, err = f.Write(raw); err != nil {
		return ErrProfile
	}
	if s.fs.syncFile(f) != nil {
		return ErrProfile
	}
	if f.Close() != nil {
		return ErrProfile
	}
	if beforeRename != nil && beforeRename() != nil {
		return ErrProfile
	}
	if s.fs.rename(tmpname, name, replace) != nil {
		return ErrProfile
	}
	if syncDir() != nil {
		return ErrProfile
	}
	return nil
}

func (s *Store) retain(kind, id string, raw []byte) error {
	if !validUUID(id) || (kind != "attempt" && kind != "result" && kind != "legacy-base") {
		return ErrProfile
	}
	sum := sha256.Sum256(raw)
	name := kind + "-" + id
	// Anchor first; an interrupted first install can only resume identical bytes.
	if err := s.writeOnce(name+".sha256", sum[:]); err != nil {
		return err
	}
	return s.writeOnce(name+".json", raw)
}

func (s *Store) retained(kind, id string) ([]byte, error) {
	if !validUUID(id) || (kind != "attempt" && kind != "result" && kind != "legacy-base") {
		return nil, ErrProfile
	}
	raw, err := s.readRecord(kind + "-" + id + ".json")
	if err != nil {
		return nil, err
	}
	anchor, err := s.readRecord(kind + "-" + id + ".sha256")
	sum := sha256.Sum256(raw)
	if err != nil || !bytes.Equal(sum[:], anchor) {
		return nil, errors.New("private record anchor mismatch")
	}
	return raw, nil
}
