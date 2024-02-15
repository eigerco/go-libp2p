//go:build js

package libp2pwebtransport

import (
	"context"
	"errors"
	"fmt"
	"go.uber.org/multierr"
	"syscall/js"

	"github.com/benbjohnson/clock"
	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	tpt "github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/security/noise"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/multiformats/go-multihash"
)

type transport struct {
	privKey ic.PrivKey
	pid     peer.ID
	clock   clock.Clock

	rcmgr network.ResourceManager
	gater connmgr.ConnectionGater

	noise *noise.Transport
}

func New(key ic.PrivKey, psk pnet.PSK, gater connmgr.ConnectionGater, rcmgr network.ResourceManager, opts ...Option) (*transport, error) {
	return new(key, psk, gater, rcmgr, nil, opts...)
}

func (t *transport) dial(ctx context.Context, tgt ma.Multiaddr, p peer.ID, sni string, certHashes []multihash.DecodedMultihash, scope network.ConnManagementScope) (tpt.CapableConn, error) {
	webtransport := js.Global().Get("WebTransport")
	if webtransport.IsUndefined() {
		return nil, fmt.Errorf("WebTransport is not supported in your browser")
	}

	var raddr string
	if sni != "" {
		raddr = sni
	} else {
		var err error
		_, raddr, err = manet.DialArgs(tgt)
		if err != nil {
			return nil, err
		}
	}

	url := fmt.Sprintf("https://%s%s?type=noise", raddr, webtransportHTTPEndpoint)

	ch := make([]any, len(certHashes))
	for i, h := range certHashes {
		if h.Code != multihash.SHA2_256 {
			// https://developer.mozilla.org/en-US/docs/Web/API/WebTransport/WebTransport#parameters
			// At time of writing, SHA-256 is the only hash algorithm listed in the specification.
			continue
		}

		ch[i] = map[string]any{
			"algorithm": "sha-256",
			"value":     byteSliceToJS(h.Digest),
		}
	}

	wt := webtransport.New(url, map[string]any{"serverCertificateHashes": ch})
	_, err := await(ctx, wt.Get("ready"))
	if err != nil {
		return nil, fmt.Errorf("initial connection: %w", err)
	}

	c := newConn(scope, t, wt, tgt, p, addr{url})

	s, err := c.openStream(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}
	defer s.Close()

	verified, err := t.verifyChallengeOnOutboundConnection(ctx, s, p, certHashes)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("verifying challenge: %w", err)
	}
	c.rpk = verified.RemotePublicKey()

	return c, nil
}

func (t *transport) Listen(a ma.Multiaddr) (tpt.Listener, error) {
	return nil, errors.New("Listen not implemented when GOOS=js.")
}

// await tries to await a piece of code, it will leave the promise in an undefined
// state if the context is canceled or expires.
func await(ctx context.Context, v js.Value) (success []js.Value, err error) {
	// This does not look very efficient but I don't care about performance right
	// now and this makes the code WAY more readable than callback hell.
	c := make(chan struct{}, 1)
	var s, f js.Func

	s = js.FuncOf(func(_ js.Value, args []js.Value) any {
		success = args
		c <- struct{}{}
		s.Release()
		f.Release()
		return nil
	})

	f = js.FuncOf(func(_ js.Value, args []js.Value) any {
		errs := make([]error, len(args))
		for _, v := range args {
			if v.Type() == js.TypeObject {
				jsonString := js.Global().Get("JSON").Call("stringify", v).String()
				errs = append(errs, errors.New(jsonString))
			} else {
				errs = append(errs, errors.New(v.String()))
			}
		}
		err = fmt.Errorf("JS catch: %w", multierr.Combine(errs...))
		c <- struct{}{}
		s.Release()
		f.Release()
		return nil
	})

	// Here we create an adhoc promise that we will race against the real one.
	// This allows us to callback into s which will Release s and f. Removing
	// references to v and hopefully allowing the JS GC to cancel the promise.
	var resolve js.Value
	capture := js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve = args[0]
		return nil
	})
	promises := js.Global().Get("Promise")
	stopper := promises.New(capture)
	capture.Release()
	promises.Call("race", []any{v, stopper}).Call("then", s, f)
	select {
	case <-ctx.Done():
		resolve.Invoke() // This will trigger s and cleanup in a thread safe manner.
		return nil, ctx.Err()
	case <-c:
		return
	}
}

/*// await tries to await a piece of code, it will leave the promise in an undefined
// state if the context is canceled or expires.
func await(ctx context.Context, v js.Value) (success []js.Value, err error) {
	// This does not look very efficient but I don't care about performance right
	// now and this makes the code WAY more readable than callback hell.
	c := make(chan struct{}, 1)
	var s, f js.Func
	s = js.FuncOf(func(_ js.Value, args []js.Value) any {
		success = args
		c <- struct{}{}
		s.Release()
		f.Release()
		return nil
	})

	f = js.FuncOf(func(_ js.Value, args []js.Value) any {
		errs := make([]error, len(args))
		for _, v := range args {
			if v.Type() == js.TypeObject {
				jsonString := js.Global().Get("JSON").Call("stringify", v).String()
				errs = append(errs, errors.New(jsonString))
			} else {
				errs = append(errs, errors.New(v.String()))
			}
		}
		err = fmt.Errorf("await catch errors: %w", multierr.Combine(errs...))
		c <- struct{}{}
		s.Release()
		f.Release()
		return nil
	})

	// Here we create an adhoc promise that we will race against the real one.
	// This allows us to callback into s which will Release s and f. Removing
	// references to v and hopefully allowing the JS GC to cancel the promise.
	var resolve js.Value
	capture := js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve = args[0]
		return nil
	})
	promises := js.Global().Get("Promise")
	stopper := promises.New(capture)
	capture.Release()
	promises.Call("race", []any{v, stopper}).Call("then", s, f)
	select {
	case <-ctx.Done():
		resolve.Invoke() // This will trigger s and cleanup in a thread safe manner.
		return nil, ctx.Err()
	case <-c:
				if len(success) > 0 {
				fmt.Printf("Success resolution of await with: \n")
				for _, val := range success {
					if val.Type() == js.TypeObject {
						fmt.Printf("%s\n", js.Global().Get("JSON").Call("stringify", val).String())
					} else {
						fmt.Printf("%s\n", val)
					}
				}
			}
		return
	}
}
*/
// await tries to await a piece of code, it will leave the promise in an undefined
// state if the context is canceled or expires.
/*func await(ctx context.Context, v js.Value) (success []js.Value, err error) {
	// Use a sync.Mutex for thread safety
	var mu sync.Mutex
	// Use a sync.WaitGroup for synchronization
	var wg sync.WaitGroup
	// Use a buffered channel for synchronization
	c := make(chan struct{}, 1)

	// Success function to handle successful promise resolution
	onSuccess := func(_ js.Value, args []js.Value) interface{} {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		if len(args) > 0 {
			success = args
		}
		c <- struct{}{}
		return nil
	}

	// Failure function to handle promise rejection
	onFailure := func(_ js.Value, args []js.Value) interface{} {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		errs := make([]error, len(args))
		for _, v := range args {
			if v.Type() == js.TypeObject {
				jsonString := js.Global().Get("JSON").Call("stringify", v).String()
				errs = append(errs, errors.New(jsonString))
			} else {
				errs = append(errs, errors.New(v.String()))
			}
		}
		err = fmt.Errorf("await catch errors: %w", multierr.Combine(errs...))
		c <- struct{}{}
		return nil
	}

	// Increment the WaitGroup counter
	wg.Add(1)

	// Register success and failure functions
	s := js.FuncOf(onSuccess)
	f := js.FuncOf(onFailure)

	// Release the functions after they've served their purpose
	defer func() {
		s.Release()
		f.Release()
	}()

	// Check if v is a promise
	if v.Type() != js.TypeObject || !v.Get("then").Truthy() {
		// If v is not a promise, directly call success callback
		onSuccess(js.Null(), []js.Value{v})
		return []js.Value{v}, nil
	}

	// Here we create an adhoc promise that we will race against the real one.
	// This allows us to callback into s which will Release s and f. Removing
	// references to v and hopefully allowing the JS GC to cancel the promise.
	var resolve js.Value
	capture := js.FuncOf(func(_ js.Value, args []js.Value) interface{} {
		mu.Lock()
		defer mu.Unlock()
		resolve = args[0]
		return nil
	})

	// Release the capture function after it has served its purpose
	defer capture.Release()

	// Create an ad hoc promise to race against the real one
	stopper := make(chan struct{})
	go func() {
		select {
		case <-stopper:
			resolve.Invoke(s, f)
		}
	}()

	// Race the stopper against the real promise
	promises := js.Global().Get("Promise")
	promises.Call("race", v, js.Global().Get("Promise").New(capture))

	select {
	case <-ctx.Done():
		// Trigger the success function to cleanup in a thread-safe manner
		onSuccess(js.Null(), nil)
		return nil, ctx.Err()
	case <-c:
		// Wait for completion of success or failure callbacks
		wg.Wait()
		fmt.Printf("Success resolution of await with: \n")
		for _, val := range success {
			if val.Type() == js.TypeObject {
				fmt.Printf("%s\n", js.Global().Get("JSON").Call("stringify", val).String())
			} else {
				fmt.Printf("%s\n", val)
			}
		}
		return success, nil
	}
}*/

func byteSliceToJS(buf []byte) js.Value {
	uint8Array := js.Global().Get("Uint8Array").New(len(buf))
	if js.CopyBytesToJS(uint8Array, buf) != len(buf) {
		panic("expected to copy all bytes")
	}
	return uint8Array
}
