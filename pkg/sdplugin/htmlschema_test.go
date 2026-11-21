package sdplugin

import (
	"testing"

	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

func TestExtractHTMLSchemaScopedAndSDPI(t *testing.T) {
	htmlDoc := []byte(`<!DOCTYPE html>
	<html><body>
	  <div data-action="com.github.mrpoundsign.towncrier.mode">
	    <div class="field">
	      <label>Key Press Behavior</label>
	      <select data-setting="targetMode">
	        <option value="cycle">Cycle</option>
	        <option value="always_on" selected>Set Always On</option>
	      </select>
	    </div>
	  </div>
	  <div data-action="com.github.mrpoundsign.towncrier.speak">
	    <div class="field">
	      <label>Button Label</label>
	      <input type="text" data-setting="label" placeholder="SPEAK" />
	    </div>
	    <div class="field">
	      <label>Speed (0.5 - 2.0)</label>
	      <input type="number" data-setting="speed" value="1.0" />
	    </div>
	  </div>
	  <input type="text" id="tc-host" value="127.0.0.1" />
	</body></html>`)

	modeFields := ExtractHTMLSchema(htmlDoc, "com.github.mrpoundsign.towncrier.mode")
	if len(modeFields) != 1 {
		t.Fatalf("expected 1 field for mode action, got %+v", modeFields)
	}
	if modeFields[0].Key != "targetMode" || modeFields[0].Label != "Key Press Behavior" ||
		modeFields[0].Type != plugin.FieldSelect || modeFields[0].Default != "always_on" {
		t.Fatalf("unexpected extracted mode field: %+v", modeFields[0])
	}

	speakFields := ExtractHTMLSchema(htmlDoc, "com.github.mrpoundsign.towncrier.speak")
	if len(speakFields) != 2 {
		t.Fatalf("expected 2 fields for speak action (excluding #tc-host), got %+v", speakFields)
	}
	if speakFields[1].Key != "speed" || speakFields[1].Type != plugin.FieldNumber || speakFields[1].Default != 1.0 {
		t.Fatalf("unexpected speed field: %+v", speakFields[1])
	}

	sdpiDoc := []byte(`
		<sdpi-item label="Endpoint">
			<sdpi-textfield setting="endpoint" placeholder="https://api"></sdpi-textfield>
		</sdpi-item>
		<sdpi-item label="Secret">
			<sdpi-password setting="apiKey"></sdpi-password>
		</sdpi-item>
		<sdpi-item label="Verbose">
			<sdpi-checkbox setting="verbose" checked></sdpi-checkbox>
		</sdpi-item>
		<sdpi-textfield setting="globalToken" global></sdpi-textfield>
	`)
	sdpiFields := ExtractHTMLSchema(sdpiDoc, "com.example.sdpi.action")
	if len(sdpiFields) != 3 {
		t.Fatalf("expected 3 sdpi fields (skipping global), got %+v", sdpiFields)
	}
	if sdpiFields[0].Label != "Endpoint" || sdpiFields[1].Type != plugin.FieldPassword || sdpiFields[2].Type != plugin.FieldBool || sdpiFields[2].Default != true {
		t.Fatalf("unexpected sdpi fields: %+v", sdpiFields)
	}
}
