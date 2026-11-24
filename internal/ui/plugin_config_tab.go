package ui

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

type pluginConfigTabView struct {
	pluginUUID string
	root       fyne.CanvasObject
	form       *schemaFormState
	formHost   *fyne.Container
	saveBtn    *widget.Button
	resetBtn   *widget.Button
}

func buildPluginConfigTab(
	win fyne.Window,
	engine *app.Engine,
	manifest plugin.Manifest,
	assetsDir func() string,
	onSuspendHotkeys func(bool),
	onStatus func(string),
) (*pluginConfigTabView, func()) {
	reportStatus := func(msg string) {
		if onStatus != nil {
			onStatus(msg)
		}
	}

	view := &pluginConfigTabView{
		pluginUUID: manifest.UUID,
		formHost:   container.NewVBox(),
	}

	view.form = newSchemaFormState(schemaFormOptions{
		win:              win,
		engine:           engine,
		assetsDir:        assetsDir,
		onStatus:         reportStatus,
		secretPluginUUID: manifest.UUID,
		onConfigAction: func(actionKey string, settings map[string]any) error {
			return engine.InvokePluginConfigAction(context.Background(), manifest.UUID, actionKey, settings)
		},
		onCaptureHotkey: func(title string, target *widget.Entry) {
			if win == nil || target == nil {
				return
			}
			openHotkeyCaptureDialog(
				win,
				title,
				target.Text,
				captureModeChord,
				onSuspendHotkeys,
				func(captured string) {
					target.SetText(captured)
					if captured == "" {
						reportStatus("Cleared shortcut — click Save to apply")
						return
					}
					reportStatus("Captured " + captured + " — click Save to apply")
				},
			)
		},
	})

	rebuildForm := func(settings map[string]any) {
		view.form.reset()
		grouped := groupSchemaFields(manifest.GlobalSettingsSchema)

		type formSection struct {
			title  string
			fields []plugin.SettingField
		}
		var sections []formSection
		if len(grouped.general) > 0 {
			sections = append(sections, formSection{title: plugin.SectionGeneral, fields: grouped.general})
		}
		for _, cg := range grouped.custom {
			if len(cg.fields) > 0 {
				sections = append(sections, formSection(cg))
			}
		}
		if len(grouped.appearance) > 0 {
			sections = append(sections, formSection{title: plugin.SectionAppearance, fields: grouped.appearance})
		}
		if len(grouped.trigger) > 0 {
			sections = append(sections, formSection{title: plugin.SectionTrigger, fields: grouped.trigger})
		}
		if len(grouped.advanced) > 0 {
			sections = append(sections, formSection{title: plugin.SectionAdvanced, fields: grouped.advanced})
		}

		if len(sections) == 1 && sections[0].title == plugin.SectionGeneral {
			view.formHost.Objects = view.form.buildFields(sections[0].fields, settings)
		} else {
			accItems := make([]*widget.AccordionItem, 0, len(sections))
			for _, sec := range sections {
				objs := view.form.buildFields(sec.fields, settings)
				item := widget.NewAccordionItem(sec.title, container.NewPadded(container.NewVBox(objs...)))
				item.Open = !strings.EqualFold(sec.title, plugin.SectionAdvanced)
				accItems = append(accItems, item)
			}
			acc := widget.NewAccordion(accItems...)
			acc.MultiOpen = true
			view.formHost.Objects = []fyne.CanvasObject{acc}
		}
		view.formHost.Refresh()
	}

	rebuildForm(engine.GlobalSettings(manifest.UUID))

	titleText := manifest.Name
	if strings.TrimSpace(manifest.Version) != "" {
		titleText += " v" + strings.TrimSpace(manifest.Version)
	}
	titleLbl := widget.NewLabelWithStyle(titleText, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	metaParts := []string{manifest.UUID}
	if strings.TrimSpace(manifest.Author) != "" {
		metaParts = append(metaParts, "by "+strings.TrimSpace(manifest.Author))
	}
	metaParts = append(metaParts, string(manifest.Provider))
	metaLbl := widget.NewLabel(strings.Join(metaParts, " • "))
	metaLbl.Wrapping = fyne.TextWrapWord

	headerItems := []fyne.CanvasObject{titleLbl, metaLbl}
	if strings.TrimSpace(manifest.Description) != "" {
		descLbl := widget.NewLabel(strings.TrimSpace(manifest.Description))
		descLbl.Wrapping = fyne.TextWrapWord
		headerItems = append(headerItems, descLbl)
	}
	headerItems = append(headerItems, widget.NewSeparator())

	schemaTypes := make(map[string]plugin.FieldType, len(manifest.GlobalSettingsSchema))
	for _, f := range manifest.GlobalSettingsSchema {
		schemaTypes[f.Key] = f.Type
	}

	view.saveBtn = widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), func() {
		collected, err := view.form.collectAndValidate()
		if err != nil {
			reportStatus(err.Error())
			return
		}
		existing := engine.GlobalSettings(manifest.UUID)
		merged := make(map[string]any, len(existing)+len(collected))
		for k, v := range existing {
			// Skip omitted keyring passwords so untouched secrets stay preserved in OS keyring.
			if schemaTypes[k] == plugin.FieldPassword {
				continue
			}
			merged[k] = v
		}
		maps.Copy(merged, collected)

		if err := engine.SaveGlobalSettings(manifest.UUID, merged); err != nil {
			reportStatus(fmt.Sprintf("Save %s settings failed: %v", manifest.Name, err))
			return
		}
		view.form.refreshDynamicFields(engine.GlobalSettings(manifest.UUID))
		view.form.refreshSecretStates()
		view.formHost.Refresh()
		reportStatus("Saved " + manifest.Name + " settings")
	})
	view.saveBtn.Importance = widget.HighImportance

	view.resetBtn = widget.NewButtonWithIcon("Reset Defaults", theme.ViewRefreshIcon(), func() {
		defaults := make(map[string]any, len(manifest.GlobalSettingsSchema))
		for _, f := range manifest.GlobalSettingsSchema {
			if isDisplayOnlyFieldType(f.Type) {
				continue
			}
			if f.Type == plugin.FieldPassword {
				defaults[f.Key] = ""
				continue
			}
			if f.Default != nil {
				defaults[f.Key] = f.Default
			}
		}
		if err := engine.SaveGlobalSettings(manifest.UUID, defaults); err != nil {
			reportStatus(fmt.Sprintf("Reset %s settings failed: %v", manifest.Name, err))
			return
		}
		rebuildForm(engine.GlobalSettings(manifest.UUID))
		reportStatus("Reset " + manifest.Name + " settings to defaults")
	})

	body := container.NewVBox(
		append(headerItems,
			view.formHost,
			widget.NewSeparator(),
			container.NewHBox(view.saveBtn, view.resetBtn),
		)...,
	)
	view.root = container.NewScroll(container.NewPadded(body))

	unsub := engine.Subscribe(func(snap app.Snapshot) {
		dispatchUI(func() {
			view.form.refreshPalette(snap.ColorPalette)
			view.form.refreshDynamicFields(engine.GlobalSettings(manifest.UUID))
			view.form.refreshSecretStatesPreserveInput()
			view.formHost.Refresh()
		})
	})

	return view, unsub
}
