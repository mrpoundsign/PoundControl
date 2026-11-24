package ui

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/pkg/builtin/core"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/secrets"
)

type interactiveConfigProvider struct {
	mu          sync.Mutex
	host        plugin.HostAPI
	manifest    plugin.Manifest
	actionCalls []struct {
		key      string
		settings map[string]any
	}
	failNextErr error
}

func (p *interactiveConfigProvider) Kind() plugin.ProviderKind        { return plugin.ProviderWasm }
func (p *interactiveConfigProvider) Name() string                     { return p.manifest.Name }
func (p *interactiveConfigProvider) Manifests() []plugin.Manifest     { return []plugin.Manifest{p.manifest} }
func (p *interactiveConfigProvider) Actions() []plugin.ActionDescriptor { return p.manifest.Actions }
func (p *interactiveConfigProvider) BindHost(host plugin.HostAPI) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}
func (p *interactiveConfigProvider) Dispatch(context.Context, plugin.ActionEvent) error { return nil }
func (p *interactiveConfigProvider) Close() error                                       { return nil }
func (p *interactiveConfigProvider) OnGlobalSettingsChanged(context.Context, string, map[string]any) error {
	return nil
}

func (p *interactiveConfigProvider) OnConfigAction(_ context.Context, _ string, actionKey string, settings map[string]any) error {
	p.mu.Lock()
	p.actionCalls = append(p.actionCalls, struct {
		key      string
		settings map[string]any
	}{key: actionKey, settings: maps.Clone(settings)})
	host := p.host
	err := p.failNextErr
	p.failNextErr = nil
	p.mu.Unlock()

	if err != nil {
		return err
	}
	if actionKey == "authConnect" && host != nil {
		next := map[string]any{
			"clientId":        settings["clientId"],
			"authStatus":      "Waiting for Twitch authorization...",
			"deviceCode":      map[string]any{"text": "WXYZ-9876", "copyable": true},
			"verificationUrl": "https://www.twitch.tv/activate",
			"authConnect":     map[string]any{"label": "Cancel Sign-In", "disabled": false},
			"authSignOut":     map[string]any{"label": "Sign Out", "disabled": true},
		}
		return host.SaveGlobalSettings(p.manifest.UUID, next)
	}
	return nil
}

func TestPluginConfigTabDynamicInfoLinkButtonAndLiveRefresh(t *testing.T) {
	drainUI()
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})
	testApp.Settings().SetTheme(&StudioTheme{})
	win := testApp.NewWindow("test")
	t.Cleanup(win.Close)

	const twitchUUID = "com.test.wasm.twitch.interactive"
	prov := &interactiveConfigProvider{
		manifest: plugin.Manifest{
			UUID:        twitchUUID,
			Name:        "Twitch",
			Version:     "1.0.0",
			Author:      "PoundControl",
			Description: "Twitch controls",
			Provider:    plugin.ProviderWasm,
			GlobalSettingsSchema: []plugin.SettingField{
				{Key: "authStatus", Label: "Connection Status", Type: plugin.FieldInfo, Section: plugin.SectionGeneral, Default: "Not signed in"},
				{Key: "deviceCode", Label: "Activation Code", Type: plugin.FieldInfo, Section: plugin.SectionGeneral, Default: map[string]any{"text": "", "copyable": true}, Help: "Copy this code."},
				{Key: "verificationUrl", Label: "Twitch Activation Page", Type: plugin.FieldLink, Section: plugin.SectionGeneral, Default: "", Placeholder: "Open twitch.tv/activate"},
				{Key: "authConnect", Label: "Sign In with Twitch", Type: plugin.FieldButton, Section: plugin.SectionGeneral, Default: map[string]any{"label": "Sign In with Twitch", "disabled": false}},
				{Key: "authSignOut", Label: "Sign Out", Type: plugin.FieldButton, Section: plugin.SectionGeneral, Default: map[string]any{"label": "Sign Out", "disabled": true}},
				{Key: "clientId", Label: "Twitch Client ID", Type: plugin.FieldText, Section: plugin.SectionAdvanced, Default: "default-cid"},
				{Key: "accessToken", Label: "OAuth Access Token", Type: plugin.FieldPassword, Section: plugin.SectionAdvanced},
			},
			Actions: []plugin.ActionDescriptor{
				{UUID: twitchUUID + ".chat", PluginUUID: twitchUUID, Name: "Chat", Category: "Twitch"},
			},
		},
	}

	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	if err := reg.RegisterProvider(prov); err != nil {
		t.Fatalf("RegisterProvider(prov) error = %v", err)
	}

	eng, err := app.NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	eng.SetSecretStore(secrets.NewMemoryStore())
	t.Cleanup(func() { _ = eng.Close() })

	var (
		statusMu   sync.Mutex
		lastStatus string
	)
	view, cleanup := buildPluginConfigTab(win, eng, prov.manifest, nil, nil, func(msg string) {
		statusMu.Lock()
		lastStatus = msg
		statusMu.Unlock()
	})
	t.Cleanup(cleanup)

	// Info, Link, and Button fields must render proper widgets (never Entry with map[...] text).
	statusLbl, ok := view.form.fields["authStatus"].widget.(*widget.Label)
	if !ok || statusLbl.Text != "Not signed in" {
		t.Fatalf("expected authStatus Label=%q, got %T %+v", "Not signed in", view.form.fields["authStatus"].widget, view.form.fields["authStatus"].widget)
	}
	codeLbl, ok := view.form.fields["deviceCode"].widget.(*widget.Label)
	if !ok {
		t.Fatalf("expected deviceCode Label, got %T", view.form.fields["deviceCode"].widget)
	}
	codeRow := view.form.fields["deviceCode"].control.(fyne.CanvasObject)
	if codeRow.Visible() {
		t.Fatal("expected empty deviceCode row hidden initially")
	}
	linkWidget, ok := view.form.fields["verificationUrl"].widget.(*widget.Hyperlink)
	if !ok {
		t.Fatalf("expected verificationUrl Hyperlink, got %T", view.form.fields["verificationUrl"].widget)
	}
	linkRow := view.form.fields["verificationUrl"].control.(fyne.CanvasObject)
	if linkRow.Visible() {
		t.Fatal("expected empty verificationUrl row hidden initially")
	}
	connectBtn, ok := view.form.fields["authConnect"].widget.(*widget.Button)
	if !ok || connectBtn.Text != "Sign In with Twitch" || connectBtn.Disabled() {
		t.Fatalf("unexpected initial authConnect button: %T text=%q disabled=%v",
			view.form.fields["authConnect"].widget, connectBtn.Text, connectBtn.Disabled())
	}
	signOutBtn, ok := view.form.fields["authSignOut"].widget.(*widget.Button)
	if !ok || signOutBtn.Text != "Sign Out" || !signOutBtn.Disabled() {
		t.Fatalf("unexpected initial authSignOut button: %T text=%q disabled=%v",
			view.form.fields["authSignOut"].widget, signOutBtn.Text, signOutBtn.Disabled())
	}

	// Unsaved clientId edit + unsaved draft secret; clicking Connect passes clientId and preserves draft secret.
	clientEntry := view.form.fields["clientId"].widget.(*widget.Entry)
	clientEntry.SetText("unsaved-cid")
	tokEntry := view.form.fields["accessToken"].secretEntry
	tokEntry.SetText("unsaved-draft-token")

	test.Tap(connectBtn)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		drainUI()
		if connectBtn.Text == "Cancel Sign-In" && !connectBtn.Disabled() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if connectBtn.Text != "Cancel Sign-In" || connectBtn.Disabled() {
		t.Fatalf("expected authConnect button updated to enabled Cancel Sign-In, got text=%q disabled=%v",
			connectBtn.Text, connectBtn.Disabled())
	}

	prov.mu.Lock()
	if len(prov.actionCalls) != 1 || prov.actionCalls[0].key != "authConnect" || prov.actionCalls[0].settings["clientId"] != "unsaved-cid" {
		t.Fatalf("unexpected OnConfigAction calls: %+v", prov.actionCalls)
	}
	host := prov.host
	prov.mu.Unlock()

	if statusLbl.Text != "Waiting for Twitch authorization..." {
		t.Fatalf("expected live updated authStatus, got %q", statusLbl.Text)
	}
	if !codeRow.Visible() || codeLbl.Text != "WXYZ-9876" {
		t.Fatalf("expected visible deviceCode WXYZ-9876, visible=%v text=%q", codeRow.Visible(), codeLbl.Text)
	}
	if !linkRow.Visible() || linkWidget.Text != "Open twitch.tv/activate" || linkWidget.URL == nil || linkWidget.URL.String() != "https://www.twitch.tv/activate" {
		t.Fatalf("expected visible verificationUrl hyperlink, visible=%v text=%q url=%v",
			linkRow.Visible(), linkWidget.Text, linkWidget.URL)
	}
	if tokEntry.Text != "unsaved-draft-token" {
		t.Fatalf("expected unsaved password draft preserved during live refresh, got %q", tokEntry.Text)
	}

	// Tap the Copy button inside the deviceCode row and verify clipboard contents.
	var copyBtn *widget.Button
	var findCopy func(fyne.CanvasObject)
	findCopy = func(o fyne.CanvasObject) {
		if o == nil || copyBtn != nil {
			return
		}
		if b, ok := o.(*widget.Button); ok && b.Text == "Copy" {
			copyBtn = b
			return
		}
		if c, ok := o.(*fyne.Container); ok {
			for _, child := range c.Objects {
				findCopy(child)
			}
		}
	}
	findCopy(codeRow)
	if copyBtn == nil || !copyBtn.Visible() {
		t.Fatalf("expected visible Copy button for copyable deviceCode")
	}
	test.Tap(copyBtn)
	if gotClip := testApp.Clipboard().Content(); gotClip != "WXYZ-9876" {
		t.Fatalf("Clipboard content = %q, want WXYZ-9876", gotClip)
	}

	// Clear unsaved draft token before simulating async OAuth completion storing real keyring token.
	tokEntry.SetText("")
	if err := host.SaveGlobalSettings(twitchUUID, map[string]any{
		"clientId":        "unsaved-cid",
		"accessToken":     "oauth-real-token",
		"userId":          "777",
		"authStatus":      "Connected as Caster (@caster)",
		"deviceCode":      "",
		"verificationUrl": "",
		"authConnect":     map[string]any{"label": "Verify Connection", "disabled": false},
		"authSignOut":     map[string]any{"label": "Sign Out", "disabled": false},
	}); err != nil {
		t.Fatalf("host.SaveGlobalSettings() error = %v", err)
	}
	drainUI()

	if statusLbl.Text != "Connected as Caster (@caster)" {
		t.Fatalf("expected connected authStatus, got %q", statusLbl.Text)
	}
	if codeRow.Visible() || linkRow.Visible() {
		t.Fatalf("expected deviceCode and verificationUrl hidden once signed in, got code=%v link=%v",
			codeRow.Visible(), linkRow.Visible())
	}
	if connectBtn.Text != "Verify Connection" || signOutBtn.Disabled() {
		t.Fatalf("expected Verify Connection enabled and Sign Out enabled, got connect=%q signOutDisabled=%v",
			connectBtn.Text, signOutBtn.Disabled())
	}
	if !eng.HasGlobalSecret(twitchUUID, "accessToken") {
		t.Fatal("expected accessToken saved in keyring")
	}

	// Saving Client ID must preserve plugin metadata (userId), keyring secret, and button maps.
	clientEntry.SetText("final-cid")
	test.Tap(view.saveBtn)
	drainUI()

	saved := eng.GlobalSettings(twitchUUID)
	if saved["clientId"] != "final-cid" || saved["userId"] != "777" || saved["accessToken"] != "oauth-real-token" {
		t.Fatalf("expected Save to preserve metadata & keyring secret alongside edited clientId, got %+v", saved)
	}
	if btnMap, ok := saved["authConnect"].(map[string]any); !ok || btnMap["label"] != "Verify Connection" {
		t.Fatalf("expected Save to preserve authConnect map without stringifying it, got %#v", saved["authConnect"])
	}
	for k, v := range saved {
		if s, isStr := v.(string); isStr && strings.Contains(s, "map[") {
			t.Fatalf("key %q corrupted with raw map string %q", k, s)
		}
	}

	// Failed config action re-enables button and reports error status cleanly.
	prov.mu.Lock()
	prov.failNextErr = errors.New("network unreachable")
	prov.mu.Unlock()
	test.Tap(connectBtn)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		drainUI()
		statusMu.Lock()
		st := lastStatus
		statusMu.Unlock()
		if strings.Contains(st, "network unreachable") && !connectBtn.Disabled() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	statusMu.Lock()
	gotStatus := lastStatus
	statusMu.Unlock()
	if !strings.Contains(gotStatus, "network unreachable") || connectBtn.Disabled() {
		t.Fatalf("expected error status and re-enabled button after failure, status=%q disabled=%v",
			gotStatus, connectBtn.Disabled())
	}

	// Plain string update to a FieldInfo whose schema Default set copyable=true inherits copyable=true.
	if err := host.SaveGlobalSettings(twitchUUID, map[string]any{
		"deviceCode": "PLAIN-STRING-CODE",
	}); err != nil {
		t.Fatalf("host.SaveGlobalSettings(plain deviceCode) error = %v", err)
	}
	drainUI()
	if !codeRow.Visible() || codeLbl.Text != "PLAIN-STRING-CODE" || !copyBtn.Visible() {
		t.Fatalf("expected plain-string deviceCode to inherit schema default copyable=true, visible=%v text=%q copyVisible=%v",
			codeRow.Visible(), codeLbl.Text, copyBtn.Visible())
	}
	test.Tap(copyBtn)
	if gotClip := testApp.Clipboard().Content(); gotClip != "PLAIN-STRING-CODE" {
		t.Fatalf("Clipboard after plain-string update = %q, want PLAIN-STRING-CODE", gotClip)
	}
}
