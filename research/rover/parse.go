package main

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"fizzbuzz/internal/fizzbuzz"
)

// This file is a copy of the parser of the service (server.go), because the
// service is package main. FuzzParseParams in the service checks it against
// url.ParseQuery.

var paramNames = [5]string{"int1", "int2", "limit", "str1", "str2"}

const maxQueryParams = 10000

func parseParams(rawQuery string) (fizzbuzz.Params, error) {
	var p fizzbuzz.Params
	vals, counts, ok := scanQuery(rawQuery)
	if !ok {
		return p, &fizzbuzz.FieldError{Param: "query", Reason: "malformed percent-encoding"}
	}
	get := func(i int) (string, error) {
		switch counts[i] {
		case 0:
			return "", &fizzbuzz.FieldError{Param: paramNames[i], Reason: "is required"}
		case 1:
			return vals[i], nil
		default:
			return "", &fizzbuzz.FieldError{Param: paramNames[i], Reason: "must be given exactly once"}
		}
	}
	getInt := func(i int) (int, error) {
		v, err := get(i)
		if err != nil {
			return 0, err
		}
		n, err := strconv.ParseInt(v, 10, 64)
		switch {
		case errors.Is(err, strconv.ErrRange):
			return 0, &fizzbuzz.FieldError{Param: paramNames[i], Reason: "is out of range"}
		case err != nil:
			return 0, &fizzbuzz.FieldError{Param: paramNames[i], Reason: "must be a base-10 integer"}
		}
		return int(n), nil
	}
	var err error
	if p.Int1, err = getInt(0); err != nil {
		return p, err
	}
	if p.Int2, err = getInt(1); err != nil {
		return p, err
	}
	if p.Limit, err = getInt(2); err != nil {
		return p, err
	}
	if p.Str1, err = get(3); err != nil {
		return p, err
	}
	p.Str2, err = get(4)
	return p, err
}

func scanQuery(q string) (vals [5]string, counts [5]uint8, ok bool) {
	if strings.Count(q, "&") >= maxQueryParams {
		return vals, counts, false
	}
	for q != "" {
		var seg string
		seg, q, _ = strings.Cut(q, "&")
		if strings.IndexByte(seg, ';') >= 0 {
			return vals, counts, false
		}
		if seg == "" {
			continue
		}
		k, v, _ := strings.Cut(seg, "=")
		k, ok := queryUnescape(k)
		if !ok {
			return vals, counts, false
		}
		v, ok = queryUnescape(v)
		if !ok {
			return vals, counts, false
		}
		for i, name := range paramNames {
			if k == name {
				if counts[i] == 0 {
					vals[i] = v
				}
				counts[i] = min(counts[i]+1, 2)
				break
			}
		}
	}
	return vals, counts, true
}

func queryUnescape(s string) (string, bool) {
	if strings.IndexByte(s, '%') < 0 && strings.IndexByte(s, '+') < 0 {
		return s, true
	}
	u, err := url.QueryUnescape(s)
	return u, err == nil
}
