package toolclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "syntra.local/mcp/gen/syntra/v1"
)

func browserConfig(apiOrigin string, caps []v1.ToolCapability, lifetime int32) BrowserConfig {
	return BrowserConfig{
		APIOrigin: apiOrigin, BrowserOrigin: "https://login.example",
		Label: "Browser tools", Capabilities: caps, LifetimeSeconds: lifetime,
	}
}

func browserConnection(owner, label string, caps []v1.ToolCapability, lifetime int32) *v1.ToolConnection {
	created := time.Now().UTC().Truncate(time.Second)
	return &v1.ToolConnection{
		Id: "33333333-3333-4333-8333-333333333333", OwnerUserId: owner,
		Label: label, Capabilities: caps,
		CreatedAt: timestamppb.New(created), ExpiresAt: timestamppb.New(created.Add(time.Duration(lifetime) * time.Second)),
		State: v1.ToolConnectionState_TOOL_CONNECTION_STATE_ACTIVE,
	}
}

func activeV1Profile(t *testing.T) *Profile {
	t.Helper()
	p := pending(t)
	created := time.Now().UTC().Truncate(time.Second)
	p.State = "active"
	p.ConnectionID = "33333333-3333-4333-8333-333333333333"
	p.CreatedAt = created.Format(time.RFC3339Nano)
	p.ExpiresAt = created.Add(time.Duration(p.Config.LifetimeSeconds) * time.Second).Format(time.RFC3339Nano)
	return p
}

func snapshotBrowserState(t *testing.T, dir string) (profile, journal, anchor []byte) {
	t.Helper()
	var err error
	profile, err = os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err = os.ReadFile(filepath.Join(dir, "attempt-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.json"))
	if err != nil {
		t.Fatal(err)
	}
	anchor, err = os.ReadFile(filepath.Join(dir, "attempt-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	return profile, journal, anchor
}

func assertBrowserStateUnchanged(t *testing.T, dir string, profile, journal, anchor []byte) {
	t.Helper()
	gotProfile, gotJournal, gotAnchor := snapshotBrowserState(t, dir)
	if !reflect.DeepEqual(gotProfile, profile) || !reflect.DeepEqual(gotJournal, journal) || !reflect.DeepEqual(gotAnchor, anchor) {
		t.Fatal("browser attempt operation changed the active profile or retained mutation journal")
	}
}

func TestPendingBrowserAttemptSurvivesFailuresRestartAndCompetition(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "profile")
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err = store.Save(activeV1Profile(t)); err != nil {
		t.Fatal(err)
	}
	journal := []byte("{\"synthetic\":\"mutation journal\"}\n")
	journalID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if err = store.retain("attempt", journalID, journal); err != nil {
		t.Fatal(err)
	}
	profileBefore, journalBefore, anchorBefore := snapshotBrowserState(t, dir)

	requested := browserConfig("https://api.example", []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ, v1.ToolCapability_TOOL_CAPABILITY_PREPARE}, 600)
	attempt, err := NewPendingBrowserAttempt(requested)
	if err != nil {
		t.Fatal(err)
	}
	encodedTarget := filepath.Join(root, "outside-target")
	if err = os.WriteFile(encodedTarget, []byte("outside data"), 0600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, browserAttemptFilename)
	if err = os.Symlink(encodedTarget, linkPath); err == nil {
		if err = store.SavePendingBrowserAttempt(attempt); err == nil {
			t.Fatal("accepted symlink pending-attempt target")
		}
		if got, readErr := os.ReadFile(encodedTarget); readErr != nil || string(got) != "outside data" {
			t.Fatal("symlink target changed", readErr)
		}
		assertBrowserStateUnchanged(t, dir, profileBefore, journalBefore, anchorBefore)
		if err = os.Remove(linkPath); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Logf("symlink fixture unavailable; continuing non-symlink recovery checks: %v", err)
	}

	if err = store.savePendingBrowserAttempt(attempt, browserAttemptWriteHooks{
		beforeRename: func() error { return errors.New("injected pre-rename interruption") },
	}); err == nil {
		t.Fatal("pre-rename interruption unexpectedly succeeded")
	}
	if _, err = store.LoadPendingBrowserAttempt(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted attempt was installed", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".record-") {
			t.Fatal("interrupted write left a temporary file", entry.Name())
		}
	}
	assertBrowserStateUnchanged(t, dir, profileBefore, journalBefore, anchorBefore)

	if err = store.savePendingBrowserAttempt(attempt, browserAttemptWriteHooks{
		syncDir: func() error { return errors.New("injected directory-flush interruption") },
	}); err == nil {
		t.Fatal("post-rename directory sync interruption unexpectedly succeeded")
	}
	retained, err := store.LoadPendingBrowserAttempt()
	if err != nil || !reflect.DeepEqual(retained, attempt) {
		t.Fatal("uncertain post-rename attempt was not recoverable", err)
	}
	if err = store.SavePendingBrowserAttempt(attempt); err != nil {
		t.Fatal("identical attempt retry did not establish durability", err)
	}
	assertBrowserStateUnchanged(t, dir, profileBefore, journalBefore, anchorBefore)

	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	retained, err = store.LoadPendingBrowserAttempt()
	if err != nil || !reflect.DeepEqual(retained, attempt) {
		t.Fatal("pending intent did not survive store restart exactly", err)
	}
	if err = store.SavePendingBrowserAttempt(attempt); err != nil {
		t.Fatal("exact retry changed the pending attempt", err)
	}
	other, err := NewPendingBrowserAttempt(requested)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SavePendingBrowserAttempt(other); !errors.Is(err, ErrBrowserAttemptExists) {
		t.Fatal("competing attempt silently replaced retained intent", err)
	}
	retained, err = store.LoadPendingBrowserAttempt()
	if err != nil || !reflect.DeepEqual(retained, attempt) {
		t.Fatal("competing attempt changed the durable intent", err)
	}
	assertBrowserStateUnchanged(t, dir, profileBefore, journalBefore, anchorBefore)
	loaded, err := store.Load()
	if err != nil || loaded.Method != "" || loaded.State != "active" {
		t.Fatal("previous active profile did not remain readable", err)
	}
	retainedJournal, err := store.retained("attempt", journalID)
	if err != nil || !reflect.DeepEqual(retainedJournal, journal) {
		t.Fatal("mutation journal did not remain readable and byte-identical", err)
	}
}

func TestV1MarshalRemainsByteCompatible(t *testing.T) {
	p := &Profile{
		Version: 1,
		Config: Config{
			APIOrigin: "https://api.example", ProviderOrigin: "https://provider.example",
			PublishableKey: "sb_publishable_fixtureOnlyPublicKey", Label: "Test tools",
			Capabilities: []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, LifetimeSeconds: 300,
		},
		AccountID: accountA, RequestID: "22222222-2222-4222-8222-222222222222",
		Secret: "syntra_tool_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", State: "pending",
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":1,"config":{"apiOrigin":"https://api.example","providerOrigin":"https://provider.example","publishableKey":"sb_publishable_fixtureOnlyPublicKey","label":"Test tools","capabilities":[1],"lifetimeSeconds":300},"accountId":"11111111-1111-4111-8111-111111111111","requestId":"22222222-2222-4222-8222-222222222222","secret":"syntra_tool_AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8","state":"pending"}`
	if string(raw) != want {
		t.Fatalf("v1 JSON changed: got %s", raw)
	}
}

func TestExplicitStoreForgetRemovesPendingBrowserIntent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	store, err := OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := NewPendingBrowserAttempt(browserConfig("https://api.example", []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 600))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SavePendingBrowserAttempt(attempt); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Remove(); err != nil {
		store.Close()
		t.Fatal("explicit forget did not clear a pending-only browser attempt", err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, browserAttemptFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("forget retained a local browser intent", err)
	}
	store, err = OpenStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Remove(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty store forget did not report missing local state", err)
	}
}

func TestBrowserGrantUsesReducedIssuedScopeInV2Profile(t *testing.T) {
	requested := browserConfig("https://api.example", []v1.ToolCapability{
		v1.ToolCapability_TOOL_CAPABILITY_READ,
		v1.ToolCapability_TOOL_CAPABILITY_PREPARE,
	}, 600)
	attempt, err := NewPendingBrowserAttempt(requested)
	if err != nil {
		t.Fatal(err)
	}
	connection := browserConnection(accountA, requested.Label, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
	profile, err := attempt.ActiveProfile(connection)
	if err != nil {
		t.Fatal("valid reduced grant was rejected", err)
	}
	if profile.Version != browserProfileVersion || profile.Method != browserLoginMethod || profile.Config.ProviderOrigin != "" || profile.Config.PublishableKey != "" || profile.Config.LifetimeSeconds != 300 || !equalCaps(profile.Config.Capabilities, connection.Capabilities) {
		t.Fatal("browser profile did not preserve the actual reduced grant")
	}
	if profile.Validate() != nil {
		t.Fatal("valid browser profile failed validation")
	}
	pendingVersion := *profile
	pendingVersion.State = "pending"
	if pendingVersion.Validate() == nil {
		t.Fatal("v2 pending state was accepted as an active profile")
	}

	for name, mutate := range map[string]func(*v1.ToolConnection){
		"scope expansion": func(q *v1.ToolConnection) {
			q.Capabilities = []v1.ToolCapability{
				v1.ToolCapability_TOOL_CAPABILITY_READ,
				v1.ToolCapability_TOOL_CAPABILITY_PREPARE,
				v1.ToolCapability_TOOL_CAPABILITY_PUBLISH,
			}
		},
		"duration expansion": func(q *v1.ToolConnection) { q.ExpiresAt = timestamppb.New(q.CreatedAt.AsTime().Add(601 * time.Second)) },
		"invalid owner":      func(q *v1.ToolConnection) { q.OwnerUserId = "not-a-canonical-uuid" },
	} {
		t.Run(name, func(t *testing.T) {
			q := browserConnection(accountA, requested.Label, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
			mutate(q)
			if _, err := attempt.ActiveProfile(q); err == nil {
				t.Fatal("accepted invalid issued metadata")
			}
		})
	}
}

type countingRoundTripper struct{ calls int }

func (c *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, errors.New("unexpected network request")
}

func writeTestProfile(t *testing.T, store *Store, raw []byte) {
	t.Helper()
	f, err := store.fs.createExclusive("profile.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(append(append([]byte(nil), raw...), '\n')); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = store.fs.syncFile(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func readTestProfile(t *testing.T, store *Store) []byte {
	t.Helper()
	f, err := store.openProfile()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func overwriteTestProfile(t *testing.T, store *Store, raw []byte) {
	t.Helper()
	if err := store.fs.remove("profile.json"); err != nil {
		t.Fatal(err)
	}
	if err := store.syncDir(); err != nil {
		t.Fatal(err)
	}
	writeTestProfile(t, store, raw)
}

func TestBrowserV2ProfileLoadsThroughSharedIdentityEntryPoint(t *testing.T) {
	client, store, fixture, otpConfig := setupClient(t)
	requested := browserConfig(otpConfig.APIOrigin, []v1.ToolCapability{
		v1.ToolCapability_TOOL_CAPABILITY_READ,
		v1.ToolCapability_TOOL_CAPABILITY_PREPARE,
	}, 600)
	attempt, err := NewPendingBrowserAttempt(requested)
	if err != nil {
		t.Fatal(err)
	}
	connection := browserConnection(accountA, requested.Label, []v1.ToolCapability{v1.ToolCapability_TOOL_CAPABILITY_READ}, 300)
	fixture.connection = connection
	profile, err := attempt.ActiveProfile(connection)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version":2`) || !strings.Contains(string(raw), `"method":"browser"`) || strings.Contains(string(raw), "providerOrigin") || strings.Contains(string(raw), "publishableKey") {
		t.Fatal("browser v2 schema lacks discriminator or contains fabricated provider credentials")
	}
	writeTestProfile(t, store, raw)
	loaded, err := store.Load()
	if err != nil || loaded.Method != browserLoginMethod || loaded.BrowserOrigin != requested.BrowserOrigin || loaded.Config.ProviderOrigin != "" || loaded.Config.PublishableKey != "" {
		t.Fatal("shared active-profile loader did not adapt browser v2", err)
	}
	if _, err = client.Identity(context.Background(), loaded); err != nil {
		t.Fatal("shared identity operation rejected browser profile", err)
	}

	before := readTestProfile(t, store)
	if err = store.Save(pending(t)); err == nil {
		t.Fatal("previous save overwrote a browser profile")
	}
	after := readTestProfile(t, store)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected previous overwrite mutated browser profile", err)
	}

	counter := &countingRoundTripper{}
	client.http.Transport = counter
	if _, err = client.CompleteLogin(context.Background(), store, otpConfig, "human-session-only"); err == nil {
		t.Fatal("OTP login replaced browser profile")
	}
	if counter.calls != 0 {
		t.Fatal("OTP attempted network access before refusing browser replacement")
	}
	final := readTestProfile(t, store)
	if !reflect.DeepEqual(before, final) {
		t.Fatal("OTP refusal changed browser profile", err)
	}

	// Reject provider-only fields in the v2 config even if the rest is valid.
	bad := strings.Replace(string(raw), `"browserOrigin":`, `"providerOrigin":"https://provider.example","browserOrigin":`, 1)
	if bad == string(raw) {
		t.Fatal("test failed to alter browser schema")
	}
	overwriteTestProfile(t, store, []byte(bad))
	if _, err = store.Load(); err == nil {
		t.Fatal("browser v2 parser accepted previous provider metadata")
	}
}
