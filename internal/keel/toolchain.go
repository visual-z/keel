package keel

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Toolchain switching, the way GOTOOLCHAIN=auto and bazelisk work: a
// repository pins one release in keel.toml, and whichever keel is on PATH
// runs that release. It is looked for in $KEEL_HOME/<version>/keel (default
// ~/.local/share/keel) and, if missing, downloaded from the GitHub release
// and checked against its checksums.txt. KEEL_TOOLCHAIN=local never
// downloads. Results still never depend on which keel happens to be on
// PATH; only the pin decides.

// invocation is the command line Main was given, re-used when switching.
var invocation []string

// execFn replaces the process; a variable so tests can observe the switch.
var execFn = syscall.Exec

const switchedEnv = "KEEL_SWITCHED"

func toolchainHome() string {
	if h := os.Getenv("KEEL_HOME"); h != "" {
		return h
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "keel")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "keel")
}

func toolchainPath(version string) string {
	return filepath.Join(toolchainHome(), version, "keel")
}

// switchTo runs the pinned release in place of this process. It returns
// only when that is impossible.
func switchTo(want, have string, stderr io.Writer) error {
	refuse := fmt.Sprintf("this is keel %s but keel.toml pins %s; install it with\n  mise use github:visual-z/keel@%s\n  or: go install github.com/visual-z/keel/cmd/keel@v%s", have, want, want, want)
	if os.Getenv(switchedEnv) != "" {
		return failf("%s\n(keel already switched to %s, and that binary reports %s)", refuse, os.Getenv(switchedEnv), have)
	}
	bin := toolchainPath(want)
	if fi, err := os.Stat(bin); err != nil || fi.Mode()&0o111 == 0 {
		if os.Getenv("KEEL_TOOLCHAIN") == "local" {
			return failf("%s\n(KEEL_TOOLCHAIN=local: %s is not installed and will not be downloaded)", refuse, bin)
		}
		fmt.Fprintf(stderr, "keel: keel.toml pins %s; installing it into %s\n", want, filepath.Dir(bin))
		if err := installRelease(want, bin); err != nil {
			return failf("%s\n(could not install it: %v)", refuse, err)
		}
	}
	env := append(os.Environ(), switchedEnv+"="+want)
	if err := execFn(bin, append([]string{bin}, invocation...), env); err != nil {
		return failf("%s\n(could not run %s: %v)", refuse, bin, err)
	}
	return nil
}

func releaseBase() string {
	if b := os.Getenv("KEEL_DOWNLOAD_BASE"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return "https://github.com/visual-z/keel/releases/download"
}

// installRelease downloads the release archive for this platform, checks it
// against the release's checksums.txt and installs its binary at bin.
func installRelease(version, bin string) error {
	name := fmt.Sprintf("keel_%s_%s_%s", version, runtime.GOOS, runtime.GOARCH)
	base := releaseBase() + "/v" + version + "/"
	sums, err := fetch(base + "checksums.txt")
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && strings.TrimPrefix(f[1], "*") == name+".tar.gz" {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt of %s lists no %s.tar.gz", version, name)
	}
	archive, err := fetch(base + name + ".tar.gz")
	if err != nil {
		return err
	}
	if got := sha256.Sum256(archive); hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s.tar.gz does not match checksums.txt", name)
	}
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s.tar.gz has no %s/keel", name, name)
		}
		if err != nil {
			return err
		}
		if h.Name != name+"/keel" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			return err
		}
		tmp := bin + ".download"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		_, cerr := io.Copy(f, io.LimitReader(tr, 256<<20))
		f.Close()
		if cerr != nil {
			os.Remove(tmp)
			return cerr
		}
		return os.Rename(tmp, bin)
	}
}

func fetch(url string) ([]byte, error) {
	c := &http.Client{Timeout: 2 * time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<20))
}
