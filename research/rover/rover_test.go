package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitPort(t *testing.T, port int) {
	for range 200 {
		if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port %d does not answer", port)
}

// startService builds and starts the service, the reference for rover.
func startService(t *testing.T) int {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fizzbuzz")
	if out, err := exec.Command("go", "build", "-o", bin, "fizzbuzz").CombinedOutput(); err != nil {
		t.Fatalf("build the service: %v\n%s", err, out)
	}
	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitPort(t, port)
	return port
}

func startRover(t *testing.T) int {
	t.Helper()
	cfg := defaultConfig()
	cfg.port = freePort(t)
	cfg.threads = 2
	if _, err := start(cfg); err != nil {
		t.Fatal(err)
	}
	waitPort(t, cfg.port)
	return cfg.port
}

var dateRE = regexp.MustCompile(`Date: [A-Za-z0-9 ,:]{25} GMT`)

// readResponse reads one response by its Content-Length, or to EOF.
func readResponse(r *bufio.Reader, head bool) ([]byte, error) {
	var b bytes.Buffer
	length := -1
	for {
		line, err := r.ReadString('\n')
		b.WriteString(line)
		if err != nil {
			return b.Bytes(), err
		}
		if line == "\r\n" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			length, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if head {
		return b.Bytes(), nil
	}
	if length < 0 {
		_, err := io.Copy(&b, r)
		return b.Bytes(), err
	}
	_, err := io.CopyN(&b, r, int64(length))
	return b.Bytes(), err
}

// exchange sends raw on one connection and reads n responses.
func exchange(t *testing.T, port int, raw string, heads []bool) []byte {
	t.Helper()
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(c, raw); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReaderSize(c, 1<<20)
	var all []byte
	for i, head := range heads {
		b, err := readResponse(r, head)
		if err != nil {
			t.Fatalf("response %d: %v (%.200q)", i, err, b)
		}
		all = append(all, b...)
	}
	return dateRE.ReplaceAll(all, []byte("Date: X"))
}

const fb = "/fizzbuzz?int1=3&int2=5&str1=fizz&str2=buzz&limit="

func get(target string, extra ...string) string {
	return "GET " + target + " HTTP/1.1\r\nHost: x\r\n" + strings.Join(extra, "") + "\r\n"
}

// TestMatchesService compares every byte of the responses, except the value
// of Date, with the service.
func TestMatchesService(t *testing.T) {
	svc, rov := startService(t), startRover(t)
	body := "int1=3&int2=5&limit=15&str1=fizz&str2=buzz"
	cases := []struct {
		name  string
		raw   string
		heads []bool
	}{
		{"small", get(fb + "100"), []bool{false}},
		{"small again, from the cache", get(fb + "100"), []bool{false}},
		{"over 2048 bytes", get(fb + "1000"), []bool{false}},
		{"in the loop", get(fb + "20000"), []bool{false}},
		{"on a goroutine", get(fb + "1000000"), []bool{false}},
		{"HEAD", "HEAD " + fb + "100 HTTP/1.1\r\nHost: x\r\n\r\n", []bool{true}},
		{"HEAD large", "HEAD " + fb + "1000000 HTTP/1.1\r\nHost: x\r\n\r\n", []bool{true}},
		{"escapes", get("/fizzbuzz?int1=%2B3&int2=05&limit=30&str1=a+%3Cb%3E&str2=%22q%22"), []bool{false}},
		{"invalid", get("/fizzbuzz?int1=0&int2=5&limit=15&str1=a&str2=b"), []bool{false}},
		{"missing", get("/fizzbuzz?int2=5"), []bool{false}},
		{"repeated", get("/fizzbuzz?int1=1&int1=1&int2=5&limit=1&str1=a&str2=b"), []bool{false}},
		{"too large", get("/fizzbuzz?int1=1&int2=1&limit=10000000&str1=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&str2=b"), []bool{false}},
		{"bad escape", get("/fizzbuzz?int1=%zz"), []bool{false}},
		{"not found", get("/nope"), []bool{false}},
		{"405", "POST /fizzbuzz HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n", []bool{false}},
		{"405 stats", "DELETE /stats HTTP/1.1\r\nHost: x\r\n\r\n", []bool{false}},
		{"healthz", get("/healthz"), []bool{false}},
		{"QUERY", "QUERY /fizzbuzz HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body, []bool{false}},
		{"QUERY 415", "QUERY /fizzbuzz HTTP/1.1\r\nHost: x\r\nContent-Length: 1\r\n\r\nx", []bool{false}},
		{"HTTP/1.0", "GET /healthz HTTP/1.0\r\n\r\n", []bool{false}},
		{"close", get(fb+"15", "Connection: close\r\n"), []bool{false}},
		{"pipelined", get(fb+"15") + get("/healthz") + get(fb+"2000") + get("/nope"), []bool{false, false, false, false}},
		{"pipelined after a goroutine", get(fb+"1000000") + get(fb+"7"), []bool{false, false}},
		{"stats", get("/stats"), []bool{false}},
	}
	for _, c := range cases {
		want := exchange(t, svc, c.raw, c.heads)
		got := exchange(t, rov, c.raw, c.heads)
		if !bytes.Equal(got, want) {
			t.Errorf("%s:\n got %.600q\nwant %.600q", c.name, got, want)
		}
	}
}

func TestStatsExactAcrossLoops(t *testing.T) {
	rov := startRover(t)
	for i := range 50 { // new connections land on both loops
		exchange(t, rov, get(fb+"15"), []bool{false})
		if i%2 == 0 {
			exchange(t, rov, get(fb+"16"), []bool{false})
		}
	}
	got := exchange(t, rov, get("/stats"), []bool{false})
	want := fmt.Sprintf(`{"params":{"int1":3,"int2":5,"limit":15,"str1":"fizz","str2":"buzz"},"hits":%d}`, 50)
	if !bytes.HasSuffix(got, []byte(want)) {
		t.Fatalf("got %s", got)
	}
}
