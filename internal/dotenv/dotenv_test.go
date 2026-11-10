package dotenv

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	input := `# A comment
APP_NAME=Shop
export MAIL_FROM=shop@example.com
EMPTY=
SPACED =  padded value   # trailing comment
HASH_IN_VALUE=abc#def
SINGLE='literal $HOME \n'
DOUBLE="line one\nline two \"quoted\""
MULTI="-----BEGIN KEY-----
abc
-----END KEY-----"
APP_NAME=Shop Two
WINDOWS=crlf` + "\r" + `
`
	vars, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	var order []string
	for _, v := range vars {
		got[v.Key] = v.Value
		order = append(order, v.Key)
	}

	want := map[string]string{
		"APP_NAME":      "Shop Two",
		"MAIL_FROM":     "shop@example.com",
		"EMPTY":         "",
		"SPACED":        "padded value",
		"HASH_IN_VALUE": "abc#def",
		"SINGLE":        `literal $HOME \n`,
		"DOUBLE":        "line one\nline two \"quoted\"",
		"MULTI":         "-----BEGIN KEY-----\nabc\n-----END KEY-----",
		"WINDOWS":       "crlf",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(got), len(want), order)
	}
	if order[0] != "APP_NAME" {
		t.Errorf("a repeated key keeps its first place: %v", order)
	}
}

func TestParseRefusesWhatIsNotAnAssignment(t *testing.T) {
	for _, bad := range []string{
		"JUST_A_WORD",
		"=value",
		"1BAD=x",
		"BAD-NAME=x",
		`OPEN="never closed`,
		"OPEN='never closed",
	} {
		if _, err := Parse(strings.NewReader(bad)); err == nil {
			t.Errorf("Parse(%q) gave no error", bad)
		}
	}
}
