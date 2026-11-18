package actionable

import (
	"errors"

	"connectrpc.com/connect"
	v1 "syntra.local/mcp/gen/syntra/v1"
)

const capabilityDenied = "capability_denied: "

// DeniedCapability identifies only a tool-scope denial, separate from other
// permission errors such as workspace membership or ticket visibility.
func DeniedCapability(cap v1.ToolCapability) error {
	return connect.NewError(connect.CodePermissionDenied, errors.New(capabilityDenied+cap.String()))
}

func CapabilityFromConnect(err error) v1.ToolCapability {
	var remote *connect.Error
	if errors.As(err, &remote) && remote.Code() == connect.CodePermissionDenied {
		for _, cap := range []v1.ToolCapability{
			v1.ToolCapability_TOOL_CAPABILITY_READ,
			v1.ToolCapability_TOOL_CAPABILITY_PREPARE,
			v1.ToolCapability_TOOL_CAPABILITY_PUBLISH,
			v1.ToolCapability_TOOL_CAPABILITY_MARK_READ,
		} {
			if remote.Message() == capabilityDenied+cap.String() {
				return cap
			}
		}
	}
	return v1.ToolCapability_TOOL_CAPABILITY_UNSPECIFIED
}
