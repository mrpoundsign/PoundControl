package plugin

import (
	"context"

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
type VisualPatch struct {
	Title    *string        `json:"title,omitempty"`
	Subtitle *string        `json:"subtitle,omitempty"`
	Color    *string        `json:"color,omitempty"`
	ImageURI *string        `json:"image,omitempty"`
	State    *int           `json:"state,omitempty"`
	Feedback map[string]any `json:"feedback,omitempty"`
	Flash    string         `json:"flash,omitempty"` // "", "ok", or "alert"
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

// HostAPI is injected into every Provider so plugins can update tiles, persist settings, log, and control navigation.
type HostAPI interface {
	UpdateVisual(context string, patch VisualPatch)
	SaveSettings(context string, settings map[string]any) error
	GetGlobalSettings(pluginUUID string) map[string]any
	SaveGlobalSettings(pluginUUID string, settings map[string]any) error
	Log(pluginUUID, message string)
	Deck() DeckControl
}
