package plugin

import (
	"context"
	"encoding/json"
	"fmt"
)

// HostHandler dispatches plugin-initiated requests (the "host RPC" direction).
// The reader goroutine inside Client invokes Dispatch whenever it sees an
// envelope with type="request" — i.e. the plugin asking the host for something.
//
// Implementations should consult the plugin's manifest (e.g. HostMethods grant)
// before serving sensitive data. A real backing implementation lands with the
// host-RPC chunk; for now the package ships only a stub.
type HostHandler interface {
	Dispatch(ctx context.Context, pluginName, method string, payload json.RawMessage) (json.RawMessage, error)
}

// nullHostHandler refuses every host.* call. It's the default for managers that
// haven't been configured with a real handler — preserves the current behavior
// (no host RPC) and makes any plugin that tries it get a clear error.
type nullHostHandler struct{}

func (nullHostHandler) Dispatch(_ context.Context, _, method string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("host RPC not configured: method %q not available", method)
}
