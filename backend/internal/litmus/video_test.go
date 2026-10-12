package litmus

import (
	"reflect"
	"testing"
)

func TestStepCaptionsSayWhatWasDoneInTheReadersWords(t *testing.T) {
	for _, c := range []struct {
		action     string
		args       []string
		role, name string
		want       string
	}{
		{"click", []string{"@e7"}, "button", "Go back", `click "Go back"`},
		{"click", []string{"@e7"}, "button", "", "click a button"},
		{"click", []string{"@e7"}, "link", "  ", "click a link"},
		{"click", []string{"@e7"}, "", "", "click an element"},
		{"fill", []string{"@e9", "hello"}, "textbox", "Message the agent", `type "hello" into "Message the agent"`},
		{"type", []string{"@e9", "hi"}, "textbox", "", `type "hi" into a text field`},
		{"select", []string{"@e3", "dark"}, "combobox", "Theme", `select "dark" in "Theme"`},
		{"press", []string{"Enter"}, "", "", "press Enter"},
		{"press", []string{"Control+K"}, "button", "ignored", "press Control+K"},
		{"click", []string{"@e7"}, "button", `Say "hi"`, `click "Say 'hi'"`},
	} {
		if got := caption(actionRec{Seq: 3, Action: c.action, Args: c.args, TargetRole: c.role, TargetName: c.name}); got != "Step 3: "+c.want {
			t.Errorf("%s %v (%s %q): %q, want Step 3: %s", c.action, c.args, c.role, c.name, got, c.want)
		}
	}
}

func TestParseRefsReadsRoleAndNameFromASnapshot(t *testing.T) {
	snap := "- generic [ref=e1]\n  - button \"Go back\" [ref=e7]\n  - link \"Say \\\"hi\\\"\" [level=2, ref=e8]\n  - button [disabled, ref=e11]\nnot a node ref=e5\n"
	want := map[string]refInfo{"@e1": {"generic", ""}, "@e7": {"button", "Go back"}, "@e8": {"link", `Say "hi"`}, "@e11": {"button", ""}}
	if got := parseRefs(snap); !reflect.DeepEqual(got, want) {
		t.Fatalf("refs %v, want %v", got, want)
	}
}
