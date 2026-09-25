package main

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"fizzbuzz/internal/fizzbuzz"
)

// parseParamsRef is the parser with url.ParseQuery that parseParams replaced.
func parseParamsRef(rawQuery string) (fizzbuzz.Params, error) {
	var p fizzbuzz.Params
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return p, &fizzbuzz.FieldError{Param: "query", Reason: "malformed percent-encoding"}
	}
	get := func(name string) (string, error) {
		switch vs := q[name]; len(vs) {
		case 0:
			return "", &fizzbuzz.FieldError{Param: name, Reason: "is required"}
		case 1:
			return vs[0], nil
		default:
			return "", &fizzbuzz.FieldError{Param: name, Reason: "must be given exactly once"}
		}
	}
	getInt := func(name string) (int, error) {
		v, err := get(name)
		if err != nil {
			return 0, err
		}
		n, err := strconv.ParseInt(v, 10, 64)
		switch {
		case errors.Is(err, strconv.ErrRange):
			return 0, &fizzbuzz.FieldError{Param: name, Reason: "is out of range"}
		case err != nil:
			return 0, &fizzbuzz.FieldError{Param: name, Reason: "must be a base-10 integer"}
		}
		return int(n), nil
	}
	if p.Int1, err = getInt("int1"); err != nil {
		return p, err
	}
	if p.Int2, err = getInt("int2"); err != nil {
		return p, err
	}
	if p.Limit, err = getInt("limit"); err != nil {
		return p, err
	}
	if p.Str1, err = get("str1"); err != nil {
		return p, err
	}
	p.Str2, err = get("str2")
	return p, err
}

func FuzzParseParams(f *testing.F) {
	for _, q := range []string{
		"int1=3&int2=5&limit=15&str1=fizz&str2=buzz",
		"int1=%2B3&int2=05&limit=15&str1=a+b&str2=%C3%A9",
		"int1=3&int1=3&int2=5&limit=1&str1=a&str2=b",
		"int1=3;int2=5", "a=%zz&int1=1", "%=1", "int1", "int1=&int2=",
		"&&int1=1&&x=y&int2=2&limit=9223372036854775808&str1=&str2=x",
		strings.Repeat("&", maxQueryParams-1), strings.Repeat("&", maxQueryParams),
		"in%74%31=1&int2=2&limit=3&str1=x&str2=y",
	} {
		f.Add(q)
	}
	f.Fuzz(func(t *testing.T, q string) {
		got, gotErr := parseParams(q)
		want, wantErr := parseParamsRef(q)
		if (gotErr == nil) != (wantErr == nil) || gotErr != nil && gotErr.Error() != wantErr.Error() {
			t.Fatalf("%q: err %v, want %v", q, gotErr, wantErr)
		}
		if gotErr == nil && got != want {
			t.Fatalf("%q: %+v, want %+v", q, got, want)
		}
	})
}
