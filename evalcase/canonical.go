package evalcase

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// canonicalJSON encodes v so that semantically equal JSON values encode to
// identical bytes: object keys are sorted (encoding/json does this for maps)
// and every number is rewritten in a canonical decimal form. This makes the
// encoding independent of whether a number is held as json.Number, float64 or
// int, and of how it was spelled in the source (1, 1.0, 1e0, 10E-1 ...).
func canonicalJSON(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return json.Marshal(canonicalNumbers(generic))
}

// canonicalNumbers rewrites, in place, every json.Number in a value produced
// by a json.Decoder with UseNumber.
func canonicalNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		return canonicalNumber(x)
	case []any:
		for i := range x {
			x[i] = canonicalNumbers(x[i])
		}
	case map[string]any:
		for k, e := range x {
			x[k] = canonicalNumbers(e)
		}
	}
	return v
}

// maxExponent bounds the exponents canonicalNumber rewrites; anything larger
// is left untouched rather than risk integer overflow.
const maxExponent = 1 << 40

// canonicalNumber returns n as [-]digits[e<exp>] where digits has no leading
// or trailing zeros and the exponent is omitted when zero, e.g. 10.50 → 105e-1,
// 1.0 → 1, 100 → 1e2, -0.0 → 0. The value is exact; no float rounding occurs.
// n must be a valid JSON number.
func canonicalNumber(n json.Number) json.Number {
	s := string(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	mant, exp := s, int64(0)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.ParseInt(s[i+1:], 10, 64)
		if err != nil || e > maxExponent || e < -maxExponent {
			return n
		}
		mant, exp = s[:i], e
	}
	intPart, frac, _ := strings.Cut(mant, ".")
	digits := strings.TrimLeft(intPart+frac, "0")
	if digits == "" {
		return "0"
	}
	exp -= int64(len(frac))
	trimmed := strings.TrimRight(digits, "0")
	exp += int64(len(digits) - len(trimmed))

	out := trimmed
	if exp != 0 {
		out += "e" + strconv.FormatInt(exp, 10)
	}
	if neg {
		out = "-" + out
	}
	return json.Number(out)
}
