package toolclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"unicode/utf8"

	v1 "syntra.local/mcp/gen/syntra/v1"
)

type browserActivationHooks struct {
	beforeProfileRename func() error
	syncProfileDir      func() error
	beforePendingRemove func() error
	syncPendingDir      func() error
}

// ReplaceTerminalBrowserAttempt replaces only the exact retained attempt after
// the caller has confirmed DENIED or EXPIRED from the API during this explicit
// login invocation.
func (s *Store) ReplaceTerminalBrowserAttempt(previous, next *PendingBrowserAttempt, expectedActive []byte, expectedActiveExists bool) error {
	if !s.valid() || previous.Validate() != nil || next.Validate() != nil {
		return ErrProfile
	}
	active, activeRaw, activeErr := s.LoadProfileSnapshot()
	activeExists := activeErr == nil
	if activeErr != nil && !errors.Is(activeErr, os.ErrNotExist) {
		return activeErr
	}
	if activeExists != expectedActiveExists || (activeExists && !bytes.Equal(activeRaw, expectedActive)) {
		return errors.New("active profile changed during terminal browser retry; retained without replacement")
	}
	current, currentRaw, err := s.LoadPendingBrowserAttemptSnapshot()
	if err != nil || !reflect.DeepEqual(current, previous) {
		return ErrBrowserAttemptExists
	}
	if activeExists {
		if active.Method == browserLoginMethod && active.RequestID == previous.RequestID && active.Secret == previous.ToolSecret {
			return ErrProfile
		}
	}
	raw, err := marshalPendingBrowserAttempt(next)
	if err != nil {
		return err
	}
	return s.replacePrivateFile(browserAttemptFilename, raw, currentRaw, s.syncDir)
}

// ActivateBrowserProfile atomically replaces the exact previously observed
// active-profile snapshot only after the attempt and issued metadata have been
// verified. It then removes the matching pending intent. Any uncertain profile
// write leaves that intent available for exact recovery.
func (s *Store) ActivateBrowserProfile(attempt *PendingBrowserAttempt, profile *Profile, expectedPrevious []byte, expectedPreviousExists bool) error {
	return s.activateBrowserProfile(attempt, profile, expectedPrevious, expectedPreviousExists, browserActivationHooks{})
}

func (s *Store) activateBrowserProfile(attempt *PendingBrowserAttempt, profile *Profile, expectedPrevious []byte, expectedPreviousExists bool, hooks browserActivationHooks) error {
	if !s.valid() || attempt.Validate() != nil || profile.Validate() != nil || !browserProfileMatchesAttempt(attempt, profile) || s.fs.checkDir() != nil {
		return ErrProfile
	}
	pending, pendingRaw, err := s.LoadPendingBrowserAttemptSnapshot()
	if err != nil || !reflect.DeepEqual(pending, attempt) {
		return ErrBrowserAttemptExists
	}
	current, currentRaw, err := s.LoadProfileSnapshot()
	currentExists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	profileRaw, err := marshalBrowserProfile(profile)
	if err != nil {
		return err
	}
	if currentExists && reflect.DeepEqual(current, profile) {
		// The profile rename succeeded before a crash or cleanup error. Verify the
		// installed state and finish only the matching pending-attempt cleanup.
		syncDir := hooks.syncProfileDir
		if syncDir == nil {
			syncDir = s.syncDir
		}
		if syncDir() != nil {
			return ErrProfile
		}
		return s.removePendingBrowserAttempt(attempt, pendingRaw, hooks)
	}
	if currentExists != expectedPreviousExists || (expectedPreviousExists && !bytes.Equal(currentRaw, expectedPrevious)) {
		return errors.New("active profile changed during browser authorization; retained without replacement")
	}
	if currentExists {
		if current.State != "active" || current.AccountID != profile.AccountID || current.Config.APIOrigin != profile.Config.APIOrigin || (current.Version == browserProfileVersion && current.BrowserOrigin != profile.BrowserOrigin) || !capabilitySubset(profile.Config.Capabilities, current.Config.Capabilities) || profile.Config.LifetimeSeconds > current.Config.LifetimeSeconds {
			return errors.New("browser grant would change account, origin or expand existing permissions")
		}
	}
	syncDir := hooks.syncProfileDir
	if syncDir == nil {
		syncDir = s.syncDir
	}
	if err = s.installProfile(profileRaw, currentRaw, currentExists, hooks.beforeProfileRename, syncDir); err != nil {
		return err
	}
	return s.removePendingBrowserAttempt(attempt, pendingRaw, hooks)
}

func browserProfileMatchesAttempt(attempt *PendingBrowserAttempt, profile *Profile) bool {
	if profile.Version != browserProfileVersion || profile.Method != browserLoginMethod || profile.State != "active" || profile.RequestID != attempt.RequestID || profile.Secret != attempt.ToolSecret || profile.Config.APIOrigin != attempt.Config.APIOrigin || profile.BrowserOrigin != attempt.Config.BrowserOrigin || profile.Config.Label != attempt.Config.Label || profile.Config.LifetimeSeconds > attempt.Config.LifetimeSeconds {
		return false
	}
	return capabilitySubset(profile.Config.Capabilities, attempt.Config.Capabilities)
}

func capabilitySubset(actual, requested []v1.ToolCapability) bool {
	if len(actual) == 0 || len(actual) > len(requested) {
		return false
	}
	for i, capability := range actual {
		if i > 0 && actual[i-1] >= capability {
			return false
		}
		found := false
		for _, requestedCapability := range requested {
			if requestedCapability == capability {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func marshalPendingBrowserAttempt(attempt *PendingBrowserAttempt) ([]byte, error) {
	if attempt == nil || attempt.Validate() != nil {
		return nil, ErrProfile
	}
	raw, err := json.Marshal(attempt)
	if err != nil || len(raw) > maxProfileBytes || !utf8.Valid(raw) || !validEscapedUnicode(raw) {
		return nil, ErrProfile
	}
	return append(raw, '\n'), nil
}

func marshalBrowserProfile(profile *Profile) ([]byte, error) {
	if profile == nil || profile.Validate() != nil || profile.Version != browserProfileVersion || profile.Method != browserLoginMethod {
		return nil, ErrProfile
	}
	raw, err := json.Marshal(profile)
	if err != nil || len(raw) > maxProfileBytes || !utf8.Valid(raw) || !validEscapedUnicode(raw) {
		return nil, ErrProfile
	}
	return append(raw, '\n'), nil
}

func (s *Store) replacePrivateFile(name string, raw, expected []byte, syncDir func() error) error {
	if len(raw) > maxProfileBytes {
		return ErrProfile
	}
	return s.writeAtomicPrivateFile(name, raw, true, expected, nil, syncDir)
}

func (s *Store) installProfile(raw, expected []byte, replace bool, beforeRename func() error, syncDir func() error) error {
	if len(raw) > maxProfileBytes {
		return ErrProfile
	}
	return s.writeAtomicPrivateFile("profile.json", raw, replace, expected, beforeRename, syncDir)
}

func (s *Store) removePendingBrowserAttempt(attempt *PendingBrowserAttempt, retainedRaw []byte, hooks browserActivationHooks) error {
	if hooks.beforePendingRemove != nil && hooks.beforePendingRemove() != nil {
		return ErrProfile
	}
	current, raw, err := s.LoadPendingBrowserAttemptSnapshot()
	if err != nil || !reflect.DeepEqual(current, attempt) {
		return ErrBrowserAttemptExists
	}
	if s.fs.remove(browserAttemptFilename) != nil {
		return ErrProfile
	}
	syncDir := hooks.syncPendingDir
	if syncDir == nil {
		syncDir = s.syncDir
	}
	if syncDir() == nil {
		return nil
	}
	// Directory-sync failure makes unlink durability uncertain. Restore the exact
	// pending bytes so a later process can safely complete recovery again.
	if len(retainedRaw) == 0 {
		retainedRaw = raw
	}
	_ = s.writeOnceWithSync(browserAttemptFilename, retainedRaw, s.syncDir)
	return ErrProfile
}

func (s *Store) SaveBrowserAttemptBeforeBegin(attempt *PendingBrowserAttempt) (*Profile, []byte, error) {
	if attempt == nil || attempt.Validate() != nil {
		return nil, nil, ErrProfile
	}
	previous, previousRaw, err := s.LoadProfileSnapshot()
	previousExists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if previousExists && (previous.State != "active" || previous.Config.APIOrigin != attempt.Config.APIOrigin || (previous.Version == browserProfileVersion && previous.BrowserOrigin != attempt.Config.BrowserOrigin) || !capabilitySubset(attempt.Config.Capabilities, previous.Config.Capabilities) || attempt.Config.LifetimeSeconds > previous.Config.LifetimeSeconds) {
		return nil, nil, errors.New("active profile origin, scope or lifetime differs; retained without replacement")
	}
	if existing, err := s.LoadPendingBrowserAttempt(); err == nil {
		if !reflect.DeepEqual(existing, attempt) {
			return nil, nil, ErrBrowserAttemptExists
		}
		if s.syncDir() != nil {
			return nil, nil, ErrProfile
		}
		return previous, previousRaw, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if err = s.SavePendingBrowserAttempt(attempt); err != nil {
		return nil, nil, err
	}
	return previous, previousRaw, nil
}

func (s *Store) BrowserSnapshot(ctx context.Context) (*Profile, []byte, *PendingBrowserAttempt, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, nil, nil, ctx.Err()
	}
	profile, raw, err := s.LoadProfileSnapshot()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, err
	}
	pending, pendingErr := s.LoadPendingBrowserAttempt()
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return nil, nil, nil, pendingErr
	}
	return profile, raw, pending, nil
}
