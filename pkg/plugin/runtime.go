package plugin

import (
	"context"
	"strings"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

// EventType names a lifecycle or user-input event dispatched to a plugin action instance.
type EventType string

const (
	EventWillAppear      EventType = "willAppear"
	EventWillDisappear   EventType = "willDisappear"
	EventKeyDown         EventType = "keyDown"
	EventKeyUp           EventType = "keyUp"
	EventDialRotate      EventType = "dialRotate"
	EventDialPress       EventType = "dialPress"
	EventSettingsChanged EventType = "didReceiveSettings"
)

// ActionEvent carries an inbound deck input or lifecycle notification for an action instance.
type ActionEvent struct {
	Type        EventType           `json:"event"`
	Context     string              `json:"context"`
	ActionUUID  string              `json:"action"`
	Controller  deck.ControllerType `json:"controller"`
	Coordinates deck.Coordinates    `json:"coordinates"`
	DialIndex   int                 `json:"dialIndex,omitempty"`
	Settings    map[string]any      `json:"settings,omitempty"`
	State       int                 `json:"state,omitempty"`
	Ticks       int                 `json:"ticks,omitempty"`
}

// VisualPatch carries partial visual updates pushed from a plugin to an active slot or dial.
// Nil pointer fields leave the corresponding visual property unchanged.
// Setting a non-empty BackgroundColor overrides the slot's configured background (and temporarily
// suppresses any configured BackgroundImage while active); setting BackgroundColor to "" restores both.
type VisualPatch struct {
	Title           *string        `json:"title,omitempty"`
	Subtitle        *string        `json:"subtitle,omitempty"`
	Color           *string        `json:"color,omitempty"`
	BackgroundColor *string        `json:"backgroundColor,omitempty"`
	ImageURI        *string        `json:"image,omitempty"`
	State           *int           `json:"state,omitempty"`
	Feedback        map[string]any `json:"feedback,omitempty"`
	Flash           string         `json:"flash,omitempty"` // "", "ok", or "alert"
}

// DeckControl exposes deck navigation and macro orchestration operations to plugins.
type DeckControl interface {
	SwitchProfile(profileRef string) error
	NextPage(wrap bool) error
	PrevPage(wrap bool) error
	GoToPage(oneBasedPage int) error
	OpenFolder(folderID, folderName string) error
	CloseFolder() error
	TriggerAction(ctx context.Context, actionUUID string, settings map[string]any) error
}

// ScopedDeckHost is optionally implemented by HostAPI providers that support
// per-viewport navigation scoping via the incoming action context.
type ScopedDeckHost interface {
	ScopedDeck(ctx context.Context) DeckControl
}

type viewportCtxKey struct{}

// WithViewport returns a context tagged with viewportID when non-empty.
func WithViewport(ctx context.Context, viewportID string) context.Context {
	trimmed := strings.TrimSpace(viewportID)
	if trimmed == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, viewportCtxKey{}, trimmed)
}

// ViewportFromContext extracts the originating viewport ID from ctx, if present.
func ViewportFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	vpID, _ := ctx.Value(viewportCtxKey{}).(string)
	return vpID
}

// DeckForContext resolves the viewport-scoped DeckControl for ctx if supported by host,
// falling back to host.Deck() otherwise.
func DeckForContext(host HostAPI, ctx context.Context) DeckControl {
	if host == nil {
		return nil
	}
	if scoped, ok := host.(ScopedDeckHost); ok {
		if ctrl := scoped.ScopedDeck(ctx); ctrl != nil {
			return ctrl
		}
	}
	return host.Deck()
}

// HostAPI is injected into every Provider so plugins can update tiles, persist settings, log, and control navigation.
type HostAPI interface {
	UpdateVisual(context string, patch VisualPatch)
	SaveSettings(context string, settings map[string]any) error
	GetGlobalSettings(pluginUUID string) map[string]any
	SaveGlobalSettings(pluginUUID string, settings map[string]any) error
	Log(pluginUUID, message string)
	Deck() DeckControl
}
