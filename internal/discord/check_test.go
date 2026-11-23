package discord

import (
	"strings"
	"testing"
)

func v2(comps ...Component) ResponseData {
	return ResponseData{Components: comps, Flags: FlagComponentsV2}
}

func TestCheckMessageCatchesBrokenLayouts(t *testing.T) {
	btn := Button(ButtonPrimary, "Go", "go")
	cases := map[string]ResponseData{
		"content with v2":       {Content: "hi", Components: []Component{Text("x")}, Flags: FlagComponentsV2},
		"section without texts": v2(Container(1, Component{Type: ComponentSection, Accessory: &btn})),
		"four texts in section": v2(Section(Thumbnail("https://i", ""), "a", "b", "c", "d")),
		"nested containers":     v2(Container(1, Container(2, Text("x")))),
		"six buttons in a row":  v2(ActionRow(btn, Button(1, "b", "b"), Button(1, "c", "c"), Button(1, "d", "d"), Button(1, "e", "e"), Button(1, "f", "f"))),
		"duplicate custom ids":  v2(Text("x"), ActionRow(btn, btn)),
		"link with custom id":   v2(Text("x"), ActionRow(Component{Type: ComponentButton, Style: ButtonLink, Label: "x", URL: "https://x", CustomID: "x"})),
		"empty gallery":         v2(Gallery()),
		"too much text":         v2(Text(strings.Repeat("a", 4001))),
		"v2 parts without flag": {Components: []Component{Text("x")}},
	}
	for name, d := range cases {
		if err := CheckMessage(d); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	var many []Component
	for i := 0; i < 41; i++ {
		many = append(many, Text("x"))
	}
	if err := CheckMessage(v2(many...)); err == nil {
		t.Error("41 components: expected an error")
	}
}

func TestCheckMessageAcceptsACard(t *testing.T) {
	d := v2(
		Container(0x7C5CFF,
			Section(Thumbnail("https://img", "cover"), "## Title", "-# sub"),
			Separator(true, SpacingSmall),
			Text("body"),
			Gallery(MediaItem{Media: Media{URL: "https://img"}}),
		),
		ActionRow(Select("menu", "Pick…", SelectOption{Label: "A", Value: "a"})),
		ActionRow(Button(ButtonPrimary, "Go", "go"), LinkButton("Site", "https://x")),
	)
	if err := CheckMessage(d); err != nil {
		t.Fatal(err)
	}
}
