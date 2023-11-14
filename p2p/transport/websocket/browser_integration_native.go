//go:build !js && !windows

package websocket

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/test"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
)

var (
	wasmBrowserTestBin     = "wasmbrowsertest"
	wasmBrowserTestDir     = filepath.Join("tools", "bin")
	wasmBrowserTestPackage = "github.com/agnivade/wasmbrowsertest"
)

func newSecureMuxer(t *testing.T) (peer.ID, []sec.SecureTransport) {
	t.Helper()
	priv, _, err := test.RandTestKeyPair(crypto.Ed25519, 256)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	noiseTpt, err := noise.New(noise.ID, priv, nil)
	require.NoError(t, err)
	return id, []sec.SecureTransport{noiseTpt}
}

// TestInBrowser is a harness that allows us to use `go test` in order to run
// WebAssembly tests in a headless browser.
func TestInBrowser(t *testing.T) {
	// ensure we have the right tools.
	err := os.MkdirAll(wasmBrowserTestDir, 0755)

	t.Logf("building %s", wasmBrowserTestPackage)
	if err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}

	cmd := exec.Command(
		"go", "build",
		"-o", wasmBrowserTestBin,
		"github.com/agnivade/wasmbrowsertest",
	)
	cmd.Dir = wasmBrowserTestDir
	err = cmd.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Log("starting server")

	// Start a transport which the browser peer will dial.
	serverDoneSignal := make(chan struct{})
	go func() {
		defer func() {
			close(serverDoneSignal)
		}()
		_, mux := newSecureMuxer(t)
		u, err := tptu.New(mux, []tptu.StreamMuxer{{ID: "/yamux", Muxer: yamux.DefaultTransport}}, nil, nil, nil)
		require.NoError(t, err, "SERVER")
		tpt, err := New(u, nil)
		require.NoError(t, err, "SERVER")
		addr, err := ma.NewMultiaddr("/ip4/127.0.0.1/tcp/5555/ws")
		require.NoError(t, err, "SERVER")
		listener, err := tpt.Listen(addr)
		require.NoError(t, err, "SERVER")
		conn, err := listener.Accept()
		require.NoError(t, err, "SERVER")
		defer conn.Close()
		stream, err := conn.OpenStream(context.Background())
		require.NoError(t, err, "SERVER, could not open stream")
		defer stream.Close()
		buf := bufio.NewReader(stream)
		_, err = stream.Write([]byte("ping\n"))
		require.NoError(t, err, "SERVER")
		msg, err := buf.ReadString('\n')
		require.NoError(t, err, "SERVER, could not read pong message")
		require.Equal(t, "pong\n", msg)
	}()

	t.Log("starting browser")

	cmd = exec.Command(
		"go", "test", "-v",
		"-exec", filepath.Join(wasmBrowserTestDir, wasmBrowserTestBin),
		"-run", "TestInBrowser",
		".",
	)
	cmd.Env = append(os.Environ(), []string{"GOOS=js", "GOARCH=wasm"}...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		formattedOutput := "\t" + strings.Join(strings.Split(string(output), "\n"), "\n\t")
		t.Log("BROWSER OUTPUT:\n", formattedOutput)
		t.Fatal("BROWSER:", err)
	}

	<-serverDoneSignal
}
