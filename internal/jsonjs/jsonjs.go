// Package jsonjs parses and serializes JSON with the observable semantics of
// JavaScript's JSON.parse and JSON.stringify, so a Go writer that edits a
// user's config file produces the same bytes the TypeScript CLI did: object key
// order is kept (array-index keys first, ascending, as JavaScript orders them),
// numbers round-trip through float64 and print in ECMAScript's shortest form,
// and strings escape only what JSON.stringify escapes.
package jsonjs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Value is a parsed JSON value: nil, bool, float64, string, []Value, or *Object.
type Value any

// Object is a JSON object that keeps JavaScript property order.
type Object struct {
	keys   []string
	values map[string]Value
}

// NewObject returns an empty object.
func NewObject() *Object {
	return &Object{values: make(map[string]Value)}
}

// Get returns the value stored under key.
func (object *Object) Get(key string) (Value, bool) {
	value, ok := object.values[key]
	return value, ok
}

// Has reports whether key is present.
func (object *Object) Has(key string) bool {
	_, ok := object.values[key]
	return ok
}

// Set stores value under key. An existing key keeps its position; a new key
// is appended, like assignment to a JavaScript object.
func (object *Object) Set(key string, value Value) {
	if _, ok := object.values[key]; !ok {
		object.keys = append(object.keys, key)
	}
	object.values[key] = value
}

// Delete removes key, like the delete operator.
func (object *Object) Delete(key string) {
	if _, ok := object.values[key]; !ok {
		return
	}
	delete(object.values, key)
	object.keys = slices.DeleteFunc(object.keys, func(candidate string) bool { return candidate == key })
}

// Len is the number of keys.
func (object *Object) Len() int {
	return len(object.keys)
}

// Keys returns the keys in JavaScript property order.
func (object *Object) Keys() []string {
	index := make([]string, 0)
	named := make([]string, 0, len(object.keys))
	for _, key := range object.keys {
		if isArrayIndex(key) {
			index = append(index, key)
		} else {
			named = append(named, key)
		}
	}
	slices.SortFunc(index, func(a, b string) int {
		left, _ := strconv.ParseUint(a, 10, 32)
		right, _ := strconv.ParseUint(b, 10, 32)
		return int(int64(left) - int64(right))
	})
	return append(index, named...)
}

// Clone returns a shallow copy, like the object spread { ...object }.
func (object *Object) Clone() *Object {
	clone := &Object{keys: slices.Clone(object.keys), values: make(map[string]Value, len(object.values))}
	maps.Copy(clone.values, object.values)
	return clone
}

// isArrayIndex reports whether key is a canonical array index below 2^32-1,
// the keys JavaScript enumerates first in ascending numeric order.
func isArrayIndex(key string) bool {
	if key == "" || len(key) > 10 || (len(key) > 1 && key[0] == '0') {
		return false
	}
	value, err := strconv.ParseUint(key, 10, 64)
	return err == nil && value < math.MaxUint32
}

// ErrSyntax reports input JSON.parse would reject.
var ErrSyntax = errors.New("invalid JSON")

// Parse decodes data like JSON.parse. Duplicate keys keep the first key's
// position and the last value.
func Parse(data []byte) (Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSyntax, err)
	}
	if _, err := decoder.Token(); err == nil {
		return nil, fmt.Errorf("%w: trailing data", ErrSyntax)
	}
	rest := bytes.TrimLeft(data[decoder.InputOffset():], " \t\r\n")
	if len(rest) > 0 {
		return nil, fmt.Errorf("%w: trailing data", ErrSyntax)
	}
	return value, nil
}

func parseValue(decoder *json.Decoder) (Value, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			return parseObject(decoder)
		case '[':
			return parseArray(decoder)
		default:
			return nil, fmt.Errorf("unexpected %q", typed)
		}
	case json.Number:
		number, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return number, nil
	default:
		return typed, nil
	}
}

func parseObject(decoder *json.Decoder) (Value, error) {
	object := NewObject()
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		value, err := parseValue(decoder)
		if err != nil {
			return nil, err
		}
		object.Set(key, value)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return object, nil
}

func parseArray(decoder *json.Decoder) (Value, error) {
	array := make([]Value, 0)
	for decoder.More() {
		value, err := parseValue(decoder)
		if err != nil {
			return nil, err
		}
		array = append(array, value)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return array, nil
}

// Stringify encodes value like JSON.stringify(value, null, indent), with
// indent spaces per level; zero produces the compact form.
func Stringify(value Value, indent int) string {
	var out strings.Builder
	writeValue(&out, value, strings.Repeat(" ", indent), "")
	return out.String()
}

// Equal reports whether a and b serialize identically, the comparison the
// TypeScript writers make with JSON.stringify(a) === JSON.stringify(b).
func Equal(a, b Value) bool {
	return Stringify(a, 0) == Stringify(b, 0)
}

func writeValue(out *strings.Builder, value Value, indent, current string) {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(typed))
	case float64:
		out.WriteString(FormatNumber(typed))
	case int:
		out.WriteString(strconv.Itoa(typed))
	case string:
		out.WriteString(Quote(typed))
	case []string:
		values := make([]Value, len(typed))
		for i, item := range typed {
			values[i] = item
		}
		writeArray(out, values, indent, current)
	case []Value:
		writeArray(out, typed, indent, current)
	case *Object:
		writeObject(out, typed, indent, current)
	default:
		panic(fmt.Sprintf("jsonjs: unsupported value %T", value))
	}
}

func writeArray(out *strings.Builder, array []Value, indent, current string) {
	if len(array) == 0 {
		out.WriteString("[]")
		return
	}
	inner := current + indent
	out.WriteByte('[')
	for i, item := range array {
		if i > 0 {
			out.WriteByte(',')
		}
		if indent != "" {
			out.WriteString("\n" + inner)
		}
		writeValue(out, item, indent, inner)
	}
	if indent != "" {
		out.WriteString("\n" + current)
	}
	out.WriteByte(']')
}

func writeObject(out *strings.Builder, object *Object, indent, current string) {
	keys := object.Keys()
	if len(keys) == 0 {
		out.WriteString("{}")
		return
	}
	inner := current + indent
	out.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		if indent != "" {
			out.WriteString("\n" + inner)
		}
		out.WriteString(Quote(key))
		out.WriteByte(':')
		if indent != "" {
			out.WriteByte(' ')
		}
		writeValue(out, object.values[key], indent, inner)
	}
	if indent != "" {
		out.WriteString("\n" + current)
	}
	out.WriteByte('}')
}

// FormatNumber prints a float64 like Number.prototype.toString, with NaN and
// the infinities as null the way JSON.stringify writes them.
func FormatNumber(number float64) string {
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "null"
	}
	if number == 0 {
		return "0"
	}
	abs := math.Abs(number)
	if abs >= 1e21 || abs < 1e-6 {
		text := strconv.FormatFloat(number, 'e', -1, 64)
		mantissa, exponent, _ := strings.Cut(text, "e")
		sign := exponent[0]
		digits := strings.TrimLeft(exponent[1:], "0")
		if sign == '+' {
			return mantissa + "e+" + digits
		}
		return mantissa + "e-" + digits
	}
	return strconv.FormatFloat(number, 'f', -1, 64)
}

// Quote encodes text as a JSON string the way JSON.stringify does.
func Quote(text string) string {
	var out strings.Builder
	out.Grow(len(text) + 2)
	out.WriteByte('"')
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&out, `\u%04x`, r)
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// AsObject returns value as an object when it is one.
func AsObject(value Value) (*Object, bool) {
	object, ok := value.(*Object)
	return object, ok
}

// AsArray returns value as an array when it is one.
func AsArray(value Value) ([]Value, bool) {
	array, ok := value.([]Value)
	return array, ok
}

// String converts a value the way JavaScript's String() does.
func String(value Value) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		if math.IsInf(typed, 1) {
			return "Infinity"
		}
		if math.IsInf(typed, -1) {
			return "-Infinity"
		}
		if math.IsNaN(typed) {
			return "NaN"
		}
		return FormatNumber(typed)
	case []Value:
		parts := make([]string, len(typed))
		for i, item := range typed {
			if item != nil {
				parts[i] = String(item)
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}

// Truthy mirrors JavaScript truthiness; an absent value is falsy.
func Truthy(value Value, present bool) bool {
	if !present {
		return false
	}
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0 && !math.IsNaN(typed)
	case string:
		return typed != ""
	default:
		return true
	}
}

// Spread copies a value into a new object like { ...value }: objects are
// cloned, arrays and strings contribute their indices, and anything else,
// including an absent value, spreads nothing.
func Spread(value Value, present bool) *Object {
	out := NewObject()
	if !present {
		return out
	}
	switch typed := value.(type) {
	case *Object:
		return typed.Clone()
	case []Value:
		for i, item := range typed {
			out.Set(strconv.Itoa(i), item)
		}
	case string:
		for i, unit := range utf16.Encode([]rune(typed)) {
			out.Set(strconv.Itoa(i), string(utf16.Decode([]uint16{unit})))
		}
	}
	return out
}

// Marshal encodes v with encoding/json's struct and value rules but
// JSON.stringify's escaping: no HTML escapes, and U+2028/U+2029 left raw. A
// non-empty indent pretty-prints like JSON.stringify(v, null, indent). The
// result carries no trailing newline.
func Marshal(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if indent != "" {
		encoder.SetIndent("", indent)
	}
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return unescapeLineSeparators(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// unescapeLineSeparators turns encoding/json's \u2028 and \u2029 escapes back
// into the raw characters. Escapes are consumed in pairs, so an escaped
// backslash followed by "u2028" is left alone.
func unescapeLineSeparators(data []byte) []byte {
	if !bytes.Contains(data, []byte(`\u202`)) {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' || i+1 >= len(data) {
			out = append(out, data[i])
			continue
		}
		if rest := data[i+1:]; len(rest) >= 5 && (string(rest[:5]) == "u2028" || string(rest[:5]) == "u2029") {
			out = utf8.AppendRune(out, rune(0x2028+int(rest[4]-'8')))
			i += 5
			continue
		}
		out = append(out, data[i], data[i+1])
		i++
	}
	return out
}

var jsDecimalLiteral = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// ToNumber converts text like JavaScript's Number(text): surrounding white
// space is ignored, an empty string is 0, Infinity and 0x/0o/0b integers are
// accepted, and anything else that is not a decimal literal is NaN.
func ToNumber(text string) float64 {
	text = TrimSpace(text)
	switch text {
	case "":
		return 0
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(text) > 2 && text[0] == '0' {
		base := 0
		switch text[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			value := 0.0
			for _, digit := range text[2:] {
				n, err := strconv.ParseUint(string(digit), base, 8)
				if err != nil {
					return math.NaN()
				}
				value = value*float64(base) + float64(n)
			}
			return value
		}
	}
	if !jsDecimalLiteral.MatchString(text) {
		return math.NaN()
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return math.NaN()
	}
	return value
}

// TrimEnd removes trailing white space like String.prototype.trimEnd.
func TrimEnd(text string) string {
	return strings.TrimRightFunc(text, isJSSpace)
}

// TrimSpace removes the white space and line terminators JavaScript's
// String.prototype.trim removes.
func TrimSpace(text string) string {
	return strings.TrimFunc(text, isJSSpace)
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00A0, 0xFEFF, 0x2028, 0x2029:
		return true
	}
	return unicode.Is(unicode.Zs, r)
}
