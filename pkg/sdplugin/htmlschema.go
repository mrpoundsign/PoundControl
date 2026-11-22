package sdplugin

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"

	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

// ExtractHTMLSchema parses a Stream Deck HTML Property Inspector document and extracts
// declarative SettingFields for actionUUID from standard inputs and sdpi-components.
func ExtractHTMLSchema(htmlBytes []byte, actionUUID string) []plugin.SettingField {
	doc, err := html.Parse(bytes.NewReader(htmlBytes))
	if err != nil {
		return nil
	}

	var (
		fields []plugin.SettingField
		seen   = make(map[string]struct{})
	)
	walkHTMLNode(doc, actionUUID, "", "", &fields, seen)
	return fields
}

func walkHTMLNode(
	n *html.Node,
	targetAction string,
	activeScope string,
	section string,
	fields *[]plugin.SettingField,
	seen map[string]struct{},
) {
	if n == nil {
		return
	}

	if n.Type == html.ElementNode {
		if hasAttr(n, "global") {
			return
		}
		if scope, ok := attrVal(n, "data-action"); ok {
			activeScope = strings.TrimSpace(scope)
			section = ""
		}
		if secAttr, ok := attrVal(n, "data-section"); ok {
			if trimmed := strings.TrimSpace(secAttr); trimmed != "" {
				section = trimmed
			}
		}
		switch strings.ToLower(n.Data) {
		case "details":
			if hdr := firstDirectChildText(n, "summary"); hdr != "" {
				section = hdr
			}
		case "fieldset":
			if hdr := firstDirectChildText(n, "legend"); hdr != "" {
				section = hdr
			}
		}
		if field, ok := parseControlNode(n, targetAction, activeScope, section); ok {
			if _, dup := seen[field.Key]; !dup {
				seen[field.Key] = struct{}{}
				*fields = append(*fields, field)
			}
		}
	}

	runningSection := section
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			if hasAttr(c, "global") {
				continue
			}
			if hdr := extractSiblingHeading(c); hdr != "" {
				runningSection = hdr
				continue
			}
		}
		walkHTMLNode(c, targetAction, activeScope, runningSection, fields, seen)
	}
}

func parseControlNode(n *html.Node, targetAction, activeScope, section string) (plugin.SettingField, bool) {
	if activeScope != "" && targetAction != "" && !strings.EqualFold(activeScope, targetAction) {
		return plugin.SettingField{}, false
	}

	tag := strings.ToLower(n.Data)
	key, ok := resolveSettingKey(n, tag)
	if !ok {
		return plugin.SettingField{}, false
	}

	field := plugin.SettingField{
		Key:     key,
		Label:   inferControlLabel(n, key),
		Section: section,
	}
	if ph, hasPH := attrVal(n, "placeholder"); hasPH {
		field.Placeholder = strings.TrimSpace(ph)
	}

	switch tag {
	case "sdpi-checkbox":
		field.Type = plugin.FieldBool
		if defStr, hasDef := attrVal(n, "default"); hasDef {
			field.Default = strings.EqualFold(strings.TrimSpace(defStr), "true")
		} else {
			field.Default = hasAttr(n, "checked")
		}
		if lbl, hasLbl := attrVal(n, "label"); hasLbl && strings.TrimSpace(lbl) != "" && field.Label == humanizeKey(key) {
			field.Label = strings.TrimSpace(lbl)
		}
		return field, true

	case "sdpi-password":
		field.Type = plugin.FieldPassword
		return field, true

	case "sdpi-textarea":
		field.Type = plugin.FieldMultiline
		if def, ok := attrVal(n, "default"); ok {
			field.Default = def
		}
		return field, true

	case "sdpi-range":
		field.Type = plugin.FieldNumber
		if def, ok := attrVal(n, "default"); ok {
			if num, valid := parseNumericLiteral(def); valid {
				field.Default = num
			}
		}
		return field, true

	case "sdpi-color":
		field.Type = plugin.FieldColor
		if def, ok := attrVal(n, "default"); ok {
			field.Default = strings.TrimSpace(def)
		}
		return field, true

	case "sdpi-select", "select":
		field.Type = plugin.FieldSelect
		opts, def := extractSelectOptions(n)
		field.Options = opts
		if defStr, hasDef := attrVal(n, "default"); hasDef && strings.TrimSpace(defStr) != "" {
			field.Default = strings.TrimSpace(defStr)
		} else if def != "" {
			field.Default = def
		}
		if len(field.Options) == 0 {
			field.Type = plugin.FieldText
		}
		return field, true

	case "textarea":
		field.Type = plugin.FieldMultiline
		if txt := strings.TrimSpace(nodeText(n)); txt != "" {
			field.Default = txt
		}
		return field, true

	case "sdpi-textfield":
		field.Type = plugin.FieldText
		if def, ok := attrVal(n, "default"); ok {
			field.Default = def
		}
		return field, true

	case "input":
		typ := "text"
		if t, hasType := attrVal(n, "type"); hasType && strings.TrimSpace(t) != "" {
			typ = strings.ToLower(strings.TrimSpace(t))
		}
		switch typ {
		case "hidden", "button", "submit", "reset", "file":
			return plugin.SettingField{}, false
		case "checkbox":
			field.Type = plugin.FieldBool
			field.Default = hasAttr(n, "checked")
		case "password":
			field.Type = plugin.FieldPassword
		case "number", "range":
			field.Type = plugin.FieldNumber
			if v, hasVal := attrVal(n, "value"); hasVal {
				if num, valid := parseNumericLiteral(v); valid {
					field.Default = num
				}
			}
		case "color":
			field.Type = plugin.FieldColor
			if v, hasVal := attrVal(n, "value"); hasVal && strings.TrimSpace(v) != "" {
				field.Default = strings.TrimSpace(v)
			}
		default:
			field.Type = plugin.FieldText
			if v, hasVal := attrVal(n, "value"); hasVal && strings.TrimSpace(v) != "" {
				field.Default = v
			}
		}
		return field, true

	default:
		return plugin.SettingField{}, false
	}
}

func resolveSettingKey(n *html.Node, tag string) (string, bool) {
	isSupported := strings.HasPrefix(tag, "sdpi-") || tag == "input" || tag == "select" || tag == "textarea"
	if !isSupported {
		return "", false
	}
	for _, attr := range []string{"data-setting", "setting"} {
		if v, ok := attrVal(n, attr); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), true
		}
	}
	if cls, ok := attrVal(n, "class"); ok && strings.Contains(cls, "sdpi-item-value") {
		if id, hasID := attrVal(n, "id"); hasID && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id), true
		}
		if name, hasName := attrVal(n, "name"); hasName && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name), true
		}
	}
	if name, ok := attrVal(n, "name"); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name), true
	}
	return "", false
}

func inferControlLabel(n *html.Node, key string) string {
	for _, a := range []string{"label", "aria-label"} {
		if v, ok := attrVal(n, a); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}

	// Check enclosing <sdpi-item label="..."> or sibling <label>/sdpi-item-label in parent container.
	for p := n.Parent; p != nil && p.Type == html.ElementNode; p = p.Parent {
		pTag := strings.ToLower(p.Data)
		if pTag == "sdpi-item" {
			if v, ok := attrVal(p, "label"); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		if lbl := findLabelInContainer(p, n); lbl != "" {
			return lbl
		}
		if pTag == "body" || pTag == "form" {
			break
		}
		if _, hasAction := attrVal(p, "data-action"); hasAction {
			break
		}
	}
	return humanizeKey(key)
}

func findLabelInContainer(container, control *html.Node) string {
	for c := container.FirstChild; c != nil; c = c.NextSibling {
		if c == control {
			continue
		}
		if c.Type != html.ElementNode {
			continue
		}
		cTag := strings.ToLower(c.Data)
		if cTag == "label" {
			if txt := strings.TrimSpace(nodeText(c)); txt != "" {
				return txt
			}
		}
		if cls, ok := attrVal(c, "class"); ok && strings.Contains(cls, "sdpi-item-label") {
			if txt := strings.TrimSpace(nodeText(c)); txt != "" {
				return txt
			}
		}
	}
	return ""
}

func extractSelectOptions(selectNode *html.Node) ([]plugin.SelectOption, string) {
	var (
		opts       []plugin.SelectOption
		defaultVal string
	)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.ToLower(n.Data) == "option" {
			label := strings.TrimSpace(nodeText(n))
			val, hasVal := attrVal(n, "value")
			if !hasVal {
				val = label
			}
			val = strings.TrimSpace(val)
			if label == "" {
				label = val
			}
			if val != "" {
				opts = append(opts, plugin.SelectOption{Label: label, Value: val})
				if hasAttr(n, "selected") || defaultVal == "" {
					defaultVal = val
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(selectNode)
	return opts, defaultVal
}

func extractSiblingHeading(n *html.Node) string {
	if n == nil || n.Type != html.ElementNode {
		return ""
	}
	if _, hasAction := attrVal(n, "data-action"); hasAction {
		return ""
	}
	if containsFormControl(n) {
		return ""
	}
	isHeading := false
	switch strings.ToLower(n.Data) {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		isHeading = true
	default:
		if cls, ok := attrVal(n, "class"); ok {
			tokens := strings.Fields(strings.ToLower(cls))
			if slices.Contains(tokens, "sdpi-heading") || slices.Contains(tokens, "sdpi-item-heading") {
				isHeading = true
			}
		}
	}
	if !isHeading {
		return ""
	}
	return strings.TrimSpace(nodeText(n))
}

func containsFormControl(root *html.Node) bool {
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		tag := strings.ToLower(c.Data)
		if strings.HasPrefix(tag, "sdpi-") || tag == "input" || tag == "select" || tag == "textarea" {
			return true
		}
		if containsFormControl(c) {
			return true
		}
	}
	return false
}

func firstDirectChildText(parent *html.Node, childTag string) string {
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && strings.EqualFold(c.Data, childTag) {
			if txt := strings.TrimSpace(nodeText(c)); txt != "" {
				return txt
			}
		}
	}
	return ""
}

func attrVal(n *html.Node, name string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val, true
		}
	}
	return "", false
}

func hasAttr(n *html.Node, name string) bool {
	_, ok := attrVal(n, name)
	return ok
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur.Type == html.TextNode {
			b.WriteString(cur.Data)
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func parseNumericLiteral(raw string) (any, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, false
	}
	if iv, err := strconv.Atoi(s); err == nil {
		return iv, true
	}
	if fv, err := strconv.ParseFloat(s, 64); err == nil {
		return fv, true
	}
	return nil, false
}

func humanizeKey(key string) string {
	var b strings.Builder
	prevUpper := false
	for i, r := range key {
		if r == '_' || r == '-' || r == '.' {
			b.WriteByte(' ')
			prevUpper = false
			continue
		}
		isUpper := unicode.IsUpper(r)
		if i > 0 && isUpper && !prevUpper {
			b.WriteByte(' ')
		}
		if i == 0 {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteRune(r)
		}
		prevUpper = isUpper
	}
	return strings.TrimSpace(b.String())
}
