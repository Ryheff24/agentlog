package jsonl

// Fields returns the raw values of the named top-level keys of a JSON object,
// in one pass, as sub-slices of obj. A key that is not there comes back nil.
//
// This exists because a log record is mostly one huge value — a tool's output,
// a file's contents — and encoding/json scans a value in full every time it is
// handed one. Reaching two levels into a 20MB record with nested Unmarshal
// calls scans those megabytes once per level. Finding the spans first means
// the decoder is only ever given the part that is actually decoded.
//
// It validates only what it must to find the spans. A record that is not an
// object, or that ends mid-value because the writer was interrupted, yields
// whatever was found before that point; the caller decodes those values with
// encoding/json, which rejects anything malformed.
//
// It stops as soon as it has every key asked for, which is what makes it
// cheap: the keys a reader dispatches on come before the megabytes. The one
// place it differs from encoding/json is a repeated key, where it keeps the
// first and encoding/json keeps the last — stopping early and honouring a
// later override cannot both be had, and these logs have neither.
func Fields(obj []byte, keys ...string) [][]byte {
	out := make([][]byte, len(keys))
	i := skipSpace(obj, 0)
	if i >= len(obj) || obj[i] != '{' {
		return out
	}
	i++
	left := len(keys)
	for left > 0 {
		if i = skipSpace(obj, i); i >= len(obj) || obj[i] == '}' {
			return out
		}
		if obj[i] != '"' {
			return out
		}
		key := span(obj, i)
		if key < 0 {
			return out
		}
		name := obj[i+1 : key-1] // the key's text, without its quotes
		if i = skipSpace(obj, key); i >= len(obj) || obj[i] != ':' {
			return out
		}
		i = skipSpace(obj, i+1)
		end := span(obj, i)
		if end < 0 {
			return out
		}
		for k, want := range keys {
			if out[k] == nil && len(want) == len(name) && want == string(name) {
				out[k] = obj[i:end]
				left--
				break
			}
		}
		if i = skipSpace(obj, end); i >= len(obj) || obj[i] != ',' {
			return out
		}
		i++
	}
	return out
}

// span returns the index just past the JSON value starting at i, or -1 if the
// value does not end inside obj.
func span(obj []byte, i int) int {
	if i >= len(obj) {
		return -1
	}
	switch obj[i] {
	case '"':
		return stringEnd(obj, i)
	case '{', '[':
		depth := 0
		for ; i < len(obj); i++ {
			switch obj[i] {
			case '"':
				if i = stringEnd(obj, i) - 1; i < 0 {
					return -1
				}
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1
				}
			}
		}
		return -1
	}
	// A number, true, false or null: it ends where the object does. One that
	// runs to the end of the input was cut off, and a cut number is still a
	// number (12 of 12345), so it is not there rather than wrong.
	for ; i < len(obj); i++ {
		switch obj[i] {
		case ',', '}', ']', ' ', '\t', '\r', '\n':
			return i
		}
	}
	return -1
}

// stringEnd returns the index just past the string starting at i.
func stringEnd(obj []byte, i int) int {
	for i++; i < len(obj); i++ {
		switch obj[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

func skipSpace(b []byte, i int) int {
	for ; i < len(b); i++ {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
		default:
			return i
		}
	}
	return i
}
