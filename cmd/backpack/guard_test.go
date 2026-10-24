package main

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var urlHost = regexp.MustCompile(`https?://([A-Za-z0-9.\-]+)(/[A-Za-z0-9._~/\-]*)?`)

// TestNoExternalHosts keeps the privacy promise honest: the code that builds
// the binary talks to this machine and nothing else.
func TestNoExternalHosts(t *testing.T) {
	root := filepath.Join("..", "..")
	scanned := 0
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(path) {
			case ".go", ".html", ".js", ".css":
				checkHosts(t, path)
				scanned++
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if scanned == 0 {
		t.Fatal("no source files scanned; wrong root?")
	}
}

func checkHosts(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		for _, m := range urlHost.FindAllStringSubmatch(line, -1) {
			host, rest := m[1], m[2]
			switch {
			case host == "127.0.0.1", host == "localhost":
			case host == "github.com" && strings.HasPrefix(rest, "/nazboyko/backpack"):
			case host == "ollama.com" && isComment(line, m[0]):
			default:
				t.Errorf("%s:%d: outside host %q", path, n, m[0])
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}

func isComment(line, url string) bool {
	i := strings.Index(line, "//")
	return i >= 0 && i < strings.Index(line, url)
}
