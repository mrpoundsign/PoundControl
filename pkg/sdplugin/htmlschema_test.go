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

func TestExtractHTMLSchemaSections(t *testing.T) {
	htmlDoc := []byte(`<!DOCTYPE html>
	<html><body>
	  <div class="sdpi-wrapper" data-action="com.example.action.a">
	    <input type="text" data-setting="mainText" />
	    <div class="sdpi-heading">Appearance</div>
	    <sdpi-item label="Active Color">
	      <sdpi-color setting="activeColor" default="#38BDF8"></sdpi-color>
	    </sdpi-item>
	    <details>
	      <summary>Advanced</summary>
	      <sdpi-item label="Retries">
	        <input type="number" data-setting="retries" value="3" />
	      </sdpi-item>
	    </details>
	    <sdpi-item label="Idle Label">
	      <sdpi-textfield setting="idleLabel"></sdpi-textfield>
	    </sdpi-item>
	    <fieldset>
	      <legend>Thresholds</legend>
	      <input type="number" data-setting="warnAt" value="80" />
	    </fieldset>
	    <div data-section="Custom Group">
	      <input type="checkbox" data-setting="flag" />
	    </div>
	  </div>
	  <div data-action="com.example.action.b">
	    <input type="text" data-setting="other" />
	  </div>
	</body></html>`)

	fieldsA := ExtractHTMLSchema(htmlDoc, "com.example.action.a")
	wantA := map[string]string{
		"mainText":    "",
		"activeColor": "Appearance",
		"retries":     "Advanced",
		"idleLabel":   "Appearance",
		"warnAt":      "Thresholds",
		"flag":        "Custom Group",
	}
	if len(fieldsA) != len(wantA) {
		t.Fatalf("expected %d fields for action A, got %+v", len(wantA), fieldsA)
	}
	for _, f := range fieldsA {
		if want, ok := wantA[f.Key]; !ok || f.Section != want {
			t.Errorf("field %q Section = %q, want %q", f.Key, f.Section, want)
		}
	}

	fieldsB := ExtractHTMLSchema(htmlDoc, "com.example.action.b")
	if len(fieldsB) != 1 || fieldsB[0].Key != "other" || fieldsB[0].Section != "" {
		t.Fatalf("expected action B heading state reset (Section empty), got %+v", fieldsB)
	}
}
