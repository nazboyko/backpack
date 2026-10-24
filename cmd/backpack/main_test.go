package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenAddr(t *testing.T) {
	for _, port := range []int{7313, 8080, 1, 65535} {
		if a := newHTTPServer(port, nil).Addr; !strings.HasPrefix(a, "127.0.0.1:") {
			t.Fatalf("port %d listens on %q", port, a)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run([]string{"--version"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "backpack ") {
		t.Fatalf("output %q", out.String())
	}
}

func TestBadPortSaysWhatToDo(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run([]string{"--port", "0", "--data", t.TempDir()}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "backpack --port") {
		t.Fatalf("got %v", err)
	}
}

func TestBusyPortStopsBeforeTheDataFolder(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	data := filepath.Join(t.TempDir(), "never-made")
	var out, errOut bytes.Buffer
	err = run([]string{"--port", fmt.Sprint(port), "--data", data, "--no-open"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "is busy") {
		t.Fatalf("got %v", err)
	}
	if _, statErr := os.Stat(data); statErr == nil {
		t.Fatal("the data folder was touched although the port was busy")
	}
}
