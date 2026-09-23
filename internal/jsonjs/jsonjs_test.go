package jsonjs

import (
	"math"
	"testing"
)

// The expectations are JSON.stringify(JSON.parse(input), null, 2) as Node prints it.
func TestRoundTripMatchesJavaScript(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"{\"b\":1,\"a\":[1.0,2e3,-0,1e21,1e-7,0.1,123456789012345678901],\"2\":\"x\",\"1\":null,\"b\":true,\"s\":\"q\\\"\\\\\\u0001 é😀</\",\"e\":{},\"z\":[]}", "{\n  \"1\": null,\n  \"2\": \"x\",\n  \"b\": true,\n  \"a\": [\n    1,\n    2000,\n    0,\n    1e+21,\n    1e-7,\n    0.1,\n    123456789012345680000\n  ],\n  \"s\": \"q\\\"\\\\\\u0001 é😀</\",\n  \"e\": {},\n  \"z\": []\n}"},
		{"[1,{\"x\":{\"y\":[]}}]", "[\n  1,\n  {\n    \"x\": {\n      \"y\": []\n    }\n  }\n]"},
		{"\"just\"", "\"just\""},
		{"1E400", "null"},
		{"[5e-324, 1.7976931348623157e308, 0.000001, 123e-20, 4294967295, 99]", "[\n  5e-324,\n  1.7976931348623157e+308,\n  0.000001,\n  1.23e-18,\n  4294967295,\n  99\n]"},
		{"{\"4294967295\":1,\"4294967294\":2,\"01\":3,\"0\":4}", "{\n  \"0\": 4,\n  \"4294967294\": 2,\n  \"4294967295\": 1,\n  \"01\": 3\n}"},
	}
	for _, tt := range tests {
		value, err := Parse([]byte(tt.input))
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", tt.input, err)
		}
		if got := Stringify(value, 2); got != tt.want {
			t.Errorf("Stringify(Parse(%q)) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseRejectsWhatJSONParseRejects(t *testing.T) {
	for _, input := range []string{"", "{", "{} x", "[1,]", "{'a':1}", "\uFEFF{}", "NaN"} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("Parse(%q) = nil error, want a syntax error", input)
		}
	}
}

func TestObjectEditsKeepJavaScriptOrder(t *testing.T) {
	object := NewObject()
	object.Set("b", 1.0)
	object.Set("a", 2.0)
	object.Set("b", 3.0)
	object.Delete("a")
	object.Set("a", 4.0)
	if got, want := Stringify(object, 0), `{"b":3,"a":4}`; got != want {
		t.Errorf("Stringify(edited) = %s, want %s", got, want)
	}
}

func TestMarshalEscapesLikeJSONStringify(t *testing.T) {
	value := struct {
		Text string `json:"text"`
		List []int  `json:"list"`
	}{Text: "a<b>&c\u2028d\u2029\\u2028\n", List: []int{1}}
	got, err := Marshal(value, "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"text\": \"a<b>&c\u2028d\u2029\\\\u2028\\n\",\n  \"list\": [\n    1\n  ]\n}"
	if string(got) != want {
		t.Errorf("Marshal() = %q, want %q", got, want)
	}
}

func TestToNumberMatchesJavaScript(t *testing.T) {
	tests := map[string]float64{
		"8": 8, " 5 ": 5, "": 0, "2.5": 2.5, "1e1": 10, ".5": 0.5, "5.": 5, "-3": -3,
		"0x10": 16, "0b11": 3, "0o17": 15, "Infinity": math.Inf(1), "-Infinity": math.Inf(-1),
		" 7\uFEFF": 7,
	}
	for text, want := range tests {
		if got := ToNumber(text); got != want {
			t.Errorf("ToNumber(%q) = %v, want %v", text, got, want)
		}
	}
	for _, text := range []string{"zero", "1_000", "inf", "NaN", "0x", "-0x10", "1e", "0x1g", "\u00857"} {
		if got := ToNumber(text); !math.IsNaN(got) {
			t.Errorf("ToNumber(%q) = %v, want NaN", text, got)
		}
	}
}
