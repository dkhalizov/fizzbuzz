// Command rawhttp is a ceiling server for the performance research. It does
// the least work that still answers HTTP/1.1 keep-alive requests from wrk.
//
//	MODE=raw-small     goroutine per connection, fixed 614-byte body
//	MODE=nethttp-small net/http with a handler that writes the same fixed body
//	MODE=raw-large     fixed body of BODY bytes, written in CHUNK-byte writes
//	MODE=sendfile-large the same body from a file in /dev/shm with sendfile
package main

import (
	"bufio"
	"bytes"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
)

const small = `["1","2","fizz","4","buzz","fizz","7","8","fizz","buzz","11","fizz","13","14","fizzbuzz","16","17","fizz","19","buzz","fizz","22","23","fizz","buzz","26","fizz","28","29","fizzbuzz","31","32","fizz","34","buzz","fizz","37","38","fizz","buzz","41","fizz","43","44","fizzbuzz","46","47","fizz","49","buzz","fizz","52","53","fizz","buzz","56","fizz","58","59","fizzbuzz","61","62","fizz","64","buzz","fizz","67","68","fizz","buzz","71","fizz","73","74","fizzbuzz","76","77","fizz","79","buzz","fizz","82","83","fizz","buzz","86","fizz","88","89","fizzbuzz","91","92","fizz","94","buzz","fizz","97","98","fizz","buzz"]`

func env(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func header(n int) []byte {
	return []byte("HTTP/1.1 200 OK\r\nAccept-Query: application/x-www-form-urlencoded\r\nContent-Length: " +
		strconv.Itoa(n) + "\r\nContent-Type: application/json\r\nDate: Fri, 25 Sep 2026 18:39:46 GMT\r\n\r\n")
}

// readRequest consumes one request head. wrk sends GET without a body.
func readRequest(r *bufio.Reader) error {
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			return err
		}
		if len(line) <= 2 {
			return nil
		}
	}
}

func serveRaw(ln net.Listener, handle func(net.Conn) error) {
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			defer c.Close()
			r := bufio.NewReaderSize(c, 4096)
			for readRequest(r) == nil {
				if handle(c) != nil {
					return
				}
			}
		}()
	}
}

func main() {
	ln, err := net.Listen("tcp", ":"+os.Getenv("PORT"))
	if err != nil {
		log.Fatal(err)
	}
	body := env("BODY", 88_888_890)
	chunk := env("CHUNK", 32<<10)
	switch os.Getenv("MODE") {
	case "raw-small":
		resp := append(header(len(small)), small...)
		serveRaw(ln, func(c net.Conn) error { _, err := c.Write(resp); return err })
	case "nethttp-small":
		b := []byte(small)
		cl := []string{strconv.Itoa(len(b))}
		ct := []string{"application/json"}
		aq := []string{"application/x-www-form-urlencoded"}
		log.Fatal(http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			h := w.Header()
			h["Content-Length"], h["Content-Type"], h["Accept-Query"] = cl, ct, aq
			_, _ = w.Write(b)
		})))
	case "raw-large":
		buf := bytes.Repeat([]byte{'x'}, chunk)
		hdr := header(body)
		serveRaw(ln, func(c net.Conn) error {
			if _, err := c.Write(hdr); err != nil {
				return err
			}
			for left := body; left > 0; left -= chunk {
				if _, err := c.Write(buf[:min(left, chunk)]); err != nil {
					return err
				}
			}
			return nil
		})
	case "sendfile-large":
		name := "/dev/shm/fizz-ceiling"
		if err := os.WriteFile(name, bytes.Repeat([]byte{'x'}, body), 0o600); err != nil {
			log.Fatal(err)
		}
		hdr := header(body)
		serveRaw(ln, func(c net.Conn) error {
			if _, err := c.Write(hdr); err != nil {
				return err
			}
			f, err := os.Open(name) // an *os.File source makes ReadFrom use sendfile
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = c.(*net.TCPConn).ReadFrom(f)
			return err
		})
	default:
		log.Fatal("unknown MODE")
	}
}
